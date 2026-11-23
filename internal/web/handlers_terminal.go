package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"nhooyr.io/websocket"

	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
)

type terminalResize struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(v)

	if v.State != vm.StateRunning {
		http.Redirect(w, r, fmt.Sprintf("/vms/%s", name), http.StatusSeeOther)
		return
	}

	data := map[string]interface{}{
		"VM": v,
	}

	if cookie, err := r.Cookie("vmm_session"); err == nil {
		data["CSRFToken"] = cookie.Value
	}

	s.renderTemplate(w, "vm_terminal.html", data)
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(v)

	if v.State != vm.StateRunning {
		http.Error(w, "VM is not running", http.StatusBadRequest)
		return
	}

	if v.IPAddress == "" {
		http.Error(w, "VM has no IP address", http.StatusBadRequest)
		return
	}

	authMethods, err := findSSHAuth(paths.SSH)
	if err != nil {
		http.Error(w, "No SSH key available: "+err.Error(), http.StatusInternalServerError)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx := r.Context()

	sshConfig := &ssh.ClientConfig{
		User:            "root",
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	addr := v.IPAddress + ":22"
	sshConn, err := dialGuestSSH(ctx, sshBootWait, func() (*ssh.Client, error) {
		return ssh.Dial("tcp", addr, sshConfig)
	}, func() {
		_ = conn.Write(ctx, websocket.MessageBinary, []byte("Waiting for SSH on "+v.Name+" to come up...\r\n"))
	})
	if err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("SSH connection failed: %v", err))
		return
	}
	defer sshConn.Close()

	session, err := sshConn.NewSession()
	if err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("SSH session failed: %v", err))
		return
	}
	defer session.Close()

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("PTY request failed: %v", err))
		return
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("stdin pipe failed: %v", err))
		return
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("stdout pipe failed: %v", err))
		return
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("stderr pipe failed: %v", err))
		return
	}

	if err := session.Shell(); err != nil {
		writeWSError(conn, ctx, fmt.Sprintf("shell start failed: %v", err))
		return
	}

	var wg sync.WaitGroup

	// SSH stdout -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// SSH stderr -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket -> SSH stdin (+ handle resize)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdin.Close()
		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if msgType == websocket.MessageText {
				var resize terminalResize
				if json.Unmarshal(data, &resize) == nil && resize.Type == "resize" {
					session.WindowChange(resize.Rows, resize.Cols)
					continue
				}
			}
			if _, err := stdin.Write(data); err != nil {
				return
			}
		}
	}()

	session.Wait()
	wg.Wait()
}

// sshBootWait is how long the terminal keeps trying a guest whose SSH isn't
// listening yet. The VM page offers the terminal as soon as the VM is
// running, which is a couple of seconds before sshd is up.
const sshBootWait = 30 * time.Second

// dialGuestSSH calls dial until it succeeds, fails for a reason a booting
// guest doesn't explain (such as authentication), wait runs out, or ctx is
// done. waiting is called once, before the first retry.
func dialGuestSSH(ctx context.Context, wait time.Duration, dial func() (*ssh.Client, error), waiting func()) (*ssh.Client, error) {
	deadline := time.Now().Add(wait)
	for attempt := 0; ; attempt++ {
		client, err := dial()
		if err == nil || !sshNotReady(err) || time.Now().After(deadline) {
			return client, err
		}
		if attempt == 0 && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(time.Second):
		}
	}
}

// sshNotReady reports whether err is what a guest that is still booting
// gives: nothing listening yet, no ARP reply yet, or sshd dropping the
// connection while it starts up.
func sshNotReady(err error) bool {
	var netErr net.Error
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) ||
		(errors.As(err, &netErr) && netErr.Timeout())
}

func writeWSError(conn *websocket.Conn, ctx context.Context, msg string) {
	log.Printf("terminal error: %s", msg)
	conn.Write(ctx, websocket.MessageBinary, []byte("\r\n"+msg+"\r\n"))
}

func findSSHAuth(sshDir string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// Try vmm managed key first (always available for VMs)
	keyPath := sshkey.PrivateKeyPath(sshDir)
	if data, err := os.ReadFile(keyPath); err == nil {
		if signer, err := ssh.ParsePrivateKey(data); err == nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	// Try ssh-agent (handles passphrase-protected keys)
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	// Also try reading user key files directly (unencrypted keys)
	var homeDir string
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if sudoUser == "root" {
			homeDir = "/root"
		} else {
			homeDir = filepath.Join("/home", sudoUser)
		}
	} else {
		homeDir, _ = os.UserHomeDir()
	}

	if homeDir != "" {
		for _, keyFile := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
			data, err := os.ReadFile(filepath.Join(homeDir, ".ssh", keyFile))
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(data)
			if err != nil {
				continue
			}
			methods = append(methods, ssh.PublicKeys(signer))
			break
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SSH key found")
	}
	return methods, nil
}
