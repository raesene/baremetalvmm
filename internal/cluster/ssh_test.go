package cluster

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raesene/baremetalvmm/internal/sshkey"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// writeKey writes priv as an OpenSSH private key, encrypted if passphrase
// isn't empty.
func writeKey(t *testing.T, path string, priv ed25519.PrivateKey, passphrase string) {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pubKey(t *testing.T, priv ed25519.PrivateKey) ssh.PublicKey {
	t.Helper()
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// keySetup is the bug report's host: a passphrase-protected user key and
// vmm's managed key in sshDir.
func keySetup(t *testing.T) (userKey, sshDir string, managed ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	userKey = filepath.Join(dir, "home", "id_rsa")
	writeKey(t, userKey, newKey(t), "s3cret")
	sshDir = filepath.Join(dir, "ssh")
	managed = newKey(t)
	writeKey(t, sshkey.PrivateKeyPath(sshDir), managed, "")
	return userKey, sshDir, managed
}

func TestSSHKeys(t *testing.T) {
	managed := sshkey.PrivateKeyPath("/var/lib/vmm/ssh")
	for _, tc := range []struct {
		user string
		want []string
	}{
		{"", []string{managed}},
		{managed, []string{managed}},
		{"/home/u/.ssh/id_ed25519", []string{"/home/u/.ssh/id_ed25519", managed}},
	} {
		got := SSHKeys(tc.user, "/var/lib/vmm/ssh")
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("SSHKeys(%q) = %v, want %v", tc.user, got, tc.want)
		}
	}
}

// A passphrase-protected user key with no agent is skipped, and the managed
// key is used.
func TestSignersFallBackToManagedKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	userKey, sshDir, managed := keySetup(t)

	signers, skipped, agentConn := NewSSHClient("192.0.2.1", SSHKeys(userKey, sshDir)).signers()
	if agentConn != nil {
		t.Fatal("got an agent connection with SSH_AUTH_SOCK unset")
	}
	if len(signers) != 1 || !bytes.Equal(signers[0].PublicKey().Marshal(), pubKey(t, managed).Marshal()) {
		t.Fatalf("want just the managed key, got %d signers", len(signers))
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], userKey) || !strings.Contains(skipped[0], "passphrase") {
		t.Errorf("want the user key skipped as passphrase protected, got %v", skipped)
	}
}

// With no usable key at all, WaitForSSH gives up at once instead of
// retrying until the timeout.
func TestWaitForSSHFailsFastWithoutUsableKey(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	userKey, _, _ := keySetup(t)

	start := time.Now()
	_, err := WaitForSSH("192.0.2.1", []string{userKey, filepath.Join(t.TempDir(), "missing")}, time.Minute)
	if !errors.Is(err, errNoUsableKey) {
		t.Fatalf("want errNoUsableKey, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("WaitForSSH took %s to fail; it should not retry", elapsed)
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("error doesn't say why the key was skipped: %v", err)
	}
}

// serveSSH runs an SSH server on localhost that accepts only allowed and
// returns its port.
func serveSSH(t *testing.T, allowed ssh.PublicKey) string {
	t.Helper()
	hostKey, err := ssh.NewSignerFromKey(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), allowed.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("key not allowed")
		},
	}
	config.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sconn, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "no channels")
				}
			}()
		}
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// serveAgent runs an ssh-agent holding key and points SSH_AUTH_SOCK at it.
func serveAgent(t *testing.T, key ed25519.PrivateKey) {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

// The managed key still gets through when an agent offers a key the VM
// refuses first. x/crypto/ssh tries each auth method type once, so this only
// works with every key in one publickey method.
func TestConnectUsesManagedKeyAfterAgentKeyRefused(t *testing.T) {
	userKey, sshDir, managed := keySetup(t)
	serveAgent(t, newKey(t))
	port := serveSSH(t, pubKey(t, managed))

	client := NewSSHClient("127.0.0.1", SSHKeys(userKey, sshDir))
	client.port = port
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	client.Close()

	// And the server really does refuse a client that only has the agent key.
	agentOnly := NewSSHClient("127.0.0.1", nil)
	agentOnly.port = port
	if err := agentOnly.Connect(); err == nil {
		agentOnly.Close()
		t.Fatal("server accepted the agent's key; the test proves nothing")
	}
}
