package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultKubeconfigPath(t *testing.T) {
	saveEnv := func(k string) func() {
		v, ok := os.LookupEnv(k)
		return func() {
			if ok {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
	}
	defer saveEnv("KUBECONFIG")()
	defer saveEnv("SUDO_USER")()
	defer saveEnv("HOME")()

	// Explicit KUBECONFIG always wins.
	os.Setenv("KUBECONFIG", "/tmp/custom.conf")
	os.Unsetenv("SUDO_USER")
	if got := defaultKubeconfigPath(); got != "/tmp/custom.conf" {
		t.Errorf("with KUBECONFIG set, got %q, want /tmp/custom.conf", got)
	}

	// sudo to root from a normal user maps to that user's home.
	os.Unsetenv("KUBECONFIG")
	os.Setenv("HOME", "/root")
	os.Setenv("SUDO_USER", "alice")
	if got := defaultKubeconfigPath(); got != "/home/alice/.kube/config" {
		t.Errorf("with SUDO_USER=alice, got %q, want /home/alice/.kube/config", got)
	}

	// HOME unset (vmm-web under systemd): must never collapse to /.kube/config.
	os.Unsetenv("KUBECONFIG")
	os.Unsetenv("SUDO_USER")
	os.Unsetenv("HOME")
	got := defaultKubeconfigPath()
	if !filepath.IsAbs(got) || got == "/.kube/config" || !strings.HasSuffix(got, "/.kube/config") {
		t.Errorf("with HOME unset, got %q; want an absolute <home>/.kube/config (root's home)", got)
	}
}
