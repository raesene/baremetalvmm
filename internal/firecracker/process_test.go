package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startSleeper starts a long-running process with the given argv. argv[0] sets
// the name the /proc inspection helpers see.
func startSleeper(t *testing.T, argv0 string, extraArgs ...string) *exec.Cmd {
	t.Helper()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	// Words after the -c script become $0, $1... for the shell, so they show
	// up in /proc/<pid>/cmdline without being interpreted.
	cmd := &exec.Cmd{
		Path: sh,
		Args: append([]string{argv0, "-c", "sleep 60"}, extraArgs...),
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Wait for the child to exec so that /proc/<pid>/cmdline reports argv0
	deadline := time.Now().Add(5 * time.Second)
	for {
		if args := procArgs(cmd.Process.Pid); len(args) > 0 && args[0] == argv0 {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake process %d never exec'd as %s", cmd.Process.Pid, argv0)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeFirecracker starts a long-running process whose argv[0] basename contains
// "firecracker" and which is passed the given socket path, so it looks like a
// real Firecracker instance to the /proc inspection helpers.
func fakeFirecracker(t *testing.T, socketPath string) *exec.Cmd {
	t.Helper()
	return startSleeper(t, "/usr/local/bin/firecracker", "--api-sock", socketPath)
}

func TestIsFirecrackerProcess(t *testing.T) {
	cmd := fakeFirecracker(t, "/tmp/test-fc.sock")

	if !IsFirecrackerProcess(cmd.Process.Pid) {
		t.Errorf("expected pid %d to be recognised as firecracker", cmd.Process.Pid)
	}
	other := startSleeper(t, "/usr/bin/some-other-daemon", "--api-sock", "/tmp/test-fc.sock")
	if IsFirecrackerProcess(other.Process.Pid) {
		t.Error("expected a non-firecracker process not to be recognised as firecracker")
	}
	if IsFirecrackerProcess(-1) {
		t.Error("expected negative pid to be rejected")
	}
}

func TestProcessMatchesSocket(t *testing.T) {
	socket := "/tmp/test-match.sock"
	cmd := fakeFirecracker(t, socket)
	pid := cmd.Process.Pid

	if !ProcessMatchesSocket(pid, socket) {
		t.Errorf("expected pid %d to match socket %s", pid, socket)
	}
	// A recycled PID belonging to a different VM must not match
	if ProcessMatchesSocket(pid, "/tmp/other-vm.sock") {
		t.Error("expected mismatched socket path to be rejected")
	}
	if ProcessMatchesSocket(pid, "") {
		t.Error("expected empty socket path to be rejected")
	}
	if ProcessMatchesSocket(0, socket) {
		t.Error("expected pid 0 to be rejected")
	}
}

func TestFindPIDForSocket(t *testing.T) {
	socket := "/tmp/test-find.sock"
	cmd := fakeFirecracker(t, socket)

	if got := FindPIDForSocket(socket); got != cmd.Process.Pid {
		t.Errorf("FindPIDForSocket = %d, want %d", got, cmd.Process.Pid)
	}
	if got := FindPIDForSocket("/tmp/no-such-vm.sock"); got != 0 {
		t.Errorf("FindPIDForSocket for unknown socket = %d, want 0", got)
	}
	if got := FindPIDForSocket(""); got != 0 {
		t.Errorf("FindPIDForSocket for empty socket = %d, want 0", got)
	}
}

func TestResolvePIDRecoversLostPID(t *testing.T) {
	socket := "/tmp/test-resolve.sock"
	cmd := fakeFirecracker(t, socket)
	pid := cmd.Process.Pid

	if got := ResolvePID(socket, pid); got != pid {
		t.Errorf("ResolvePID with correct pid = %d, want %d", got, pid)
	}
	// A VM record whose PID was cleared must still find its process
	if got := ResolvePID(socket, 0); got != pid {
		t.Errorf("ResolvePID with lost pid = %d, want %d", got, pid)
	}
	// A stale PID pointing at something else must fall back to the scan
	if got := ResolvePID(socket, os.Getpid()); got != pid {
		t.Errorf("ResolvePID with stale pid = %d, want %d", got, pid)
	}
}

func TestSignalAndWait(t *testing.T) {
	socket := "/tmp/test-signal.sock"
	cmd := fakeFirecracker(t, socket)
	pid := cmd.Process.Pid

	if !signalAndWait(pid, socket, syscall.SIGKILL, 5*time.Second) {
		t.Fatalf("expected pid %d to exit after SIGKILL", pid)
	}
	_, _ = cmd.Process.Wait()

	if ProcessMatchesSocket(pid, socket) {
		t.Error("expected process to be gone after SIGKILL")
	}
	// Signalling a process that is already gone reports success
	if !signalAndWait(pid, socket, syscall.SIGKILL, time.Second) {
		t.Error("expected signalAndWait on a dead process to report exit")
	}
}

func TestIsRunningUsesProcessNotSocketFile(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "missing.sock")
	cmd := fakeFirecracker(t, socket)
	c := NewClient()

	// The socket file was never created, but the process is alive: the VM
	// must still be reported as running so it is not silently orphaned.
	if !c.IsRunning(socket, cmd.Process.Pid) {
		t.Error("expected IsRunning to be true when the process is alive without a socket file")
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !waitForExit(cmd.Process.Pid, socket, 5*time.Second) {
		t.Fatal("fake firecracker did not exit")
	}
	if c.IsRunning(socket, cmd.Process.Pid) {
		t.Error("expected IsRunning to be false once the process is gone")
	}
}
