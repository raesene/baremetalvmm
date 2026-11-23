package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/raesene/baremetalvmm/internal/vm"
)

func TestRespondVMAction(t *testing.T) {
	s := &Server{}
	v := &vm.VM{Name: "web1", State: vm.StateRunning}

	// The VM page's Start/Stop forms must come back to the VM page, not the list
	rec := httptest.NewRecorder()
	s.respondVMAction(rec, httptest.NewRequest(http.MethodPost, "/vms/web1/start", nil), v)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/vms/web1" {
		t.Errorf("form post: got %d Location %q, want 303 to /vms/web1", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	s.respondVMAction(rec, httptest.NewRequest(http.MethodPost, "/api/v1/vms/web1/start", nil), v)
	if rec.Code != http.StatusOK {
		t.Fatalf("API: got status %d, want 200", rec.Code)
	}
	var got vm.VM
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("API: response is not a VM: %v", err)
	}
	if got.Name != "web1" || got.State != vm.StateRunning {
		t.Errorf("API: got %s/%s, want web1/running", got.Name, got.State)
	}
}

func TestDialGuestSSH(t *testing.T) {
	refused := fmt.Errorf("dial tcp 172.16.0.2:22: connect: %w", syscall.ECONNREFUSED)
	ok := &ssh.Client{}

	t.Run("waits for a booting guest", func(t *testing.T) {
		calls, waited := 0, 0
		client, err := dialGuestSSH(context.Background(), time.Minute, func() (*ssh.Client, error) {
			calls++
			if calls < 3 {
				return nil, refused
			}
			return ok, nil
		}, func() { waited++ })
		if err != nil || client != ok {
			t.Fatalf("got %v, %v; want the client", client, err)
		}
		if calls != 3 || waited != 1 {
			t.Errorf("calls=%d waited=%d, want 3 and 1", calls, waited)
		}
	})

	t.Run("does not retry auth failures", func(t *testing.T) {
		calls := 0
		authErr := errors.New("ssh: handshake failed: ssh: unable to authenticate")
		_, err := dialGuestSSH(context.Background(), time.Minute, func() (*ssh.Client, error) {
			calls++
			return nil, authErr
		}, nil)
		if !errors.Is(err, authErr) || calls != 1 {
			t.Errorf("got %v after %d calls, want the auth error after 1", err, calls)
		}
	})

	t.Run("gives up after the wait", func(t *testing.T) {
		calls := 0
		_, err := dialGuestSSH(context.Background(), 0, func() (*ssh.Client, error) {
			calls++
			return nil, refused
		}, nil)
		if !errors.Is(err, syscall.ECONNREFUSED) || calls != 1 {
			t.Errorf("got %v after %d calls, want refused after 1", err, calls)
		}
	})

	t.Run("stops when the browser goes away", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := dialGuestSSH(ctx, time.Minute, func() (*ssh.Client, error) {
			return nil, refused
		}, nil)
		if err == nil {
			t.Error("expected an error once the context is done")
		}
	})
}

func TestSSHNotReady(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("connect: %w", syscall.ECONNREFUSED),
		fmt.Errorf("connect: %w", syscall.EHOSTUNREACH),
		fmt.Errorf("ssh: handshake failed: %w", io.EOF),
	} {
		if !sshNotReady(err) {
			t.Errorf("sshNotReady(%v) = false, want true", err)
		}
	}
	if sshNotReady(errors.New("ssh: unable to authenticate")) {
		t.Error("auth failure should not count as a booting guest")
	}
}
