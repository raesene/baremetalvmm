package cluster

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/raesene/baremetalvmm/internal/vm"
)

func saveVM(t *testing.T, dir, name, ip string, state vm.State) {
	t.Helper()
	v := vm.NewVM(name)
	v.IPAddress = ip
	v.State = state
	if err := v.Save(dir); err != nil {
		t.Fatal(err)
	}
}

func TestStatus(t *testing.T) {
	dir := t.TempDir()
	cl := NewCluster("c1", 2, "1.37.1", "", "")
	cl.AdminVM = "c1-admin"
	cl.State = StateRunning
	cl.JoinToken = "abcdef.0123456789abcdef"
	cl.JoinCAHash = "sha256:deadbeef"

	// No VM process exists in the test, so only states that don't claim a
	// running process survive UpdateVMState
	saveVM(t, dir, "c1-control-plane", "172.16.0.10", vm.StateStopped)
	saveVM(t, dir, "c1-worker-1", "", vm.StateCreated)
	saveVM(t, dir, "c1-admin", "172.16.0.13", vm.StateStopped)
	// c1-worker-2 has no VM record

	st := cl.Status(dir)

	if st.State != StateDegraded {
		t.Errorf("state = %q, want degraded (cluster running, VMs not)", st.State)
	}
	if st.Type != DistroKubeadm || st.CNI != CNICilium || st.K8sVersion != "1.37.1" {
		t.Errorf("type/cni/version = %s/%s/%s", st.Type, st.CNI, st.K8sVersion)
	}
	if st.Context != "vmm-c1" {
		t.Errorf("context = %q", st.Context)
	}
	// ControlPlaneIP isn't stored until provisioning; fall back to the VM's
	if st.ControlPlaneIP != "172.16.0.10" {
		t.Errorf("control_plane_ip = %q, want the control plane VM's IP", st.ControlPlaneIP)
	}

	want := []NodeStat{
		{RoleControlPlane, MemberVM{"c1-control-plane", "172.16.0.10", "stopped"}},
		{RoleWorker, MemberVM{"c1-worker-1", "", "created"}},
		{RoleWorker, MemberVM{"c1-worker-2", "", VMStateMissing}},
	}
	if len(st.Nodes) != len(want) {
		t.Fatalf("nodes = %+v", st.Nodes)
	}
	for i := range want {
		if st.Nodes[i] != want[i] {
			t.Errorf("node %d = %+v, want %+v", i, st.Nodes[i], want[i])
		}
	}
	if a := st.AdminWorkstation; a == nil || *a != (MemberVM{"c1-admin", "172.16.0.13", "stopped"}) {
		t.Errorf("admin = %+v", a)
	}

	out, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{cl.JoinToken, cl.JoinCAHash, "join_token", "join_ca_hash"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("JSON contains %q: %s", secret, out)
		}
	}
	for _, key := range []string{`"role":"control-plane"`, `"role":"worker"`, `"ip":""`, `"admin_workstation":{`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("JSON missing %s: %s", key, out)
		}
	}
}

func TestStatusOpenShift(t *testing.T) {
	cl := NewCluster("ocp", 3, "", DistroOpenShift, "")
	cl.OpenShiftVer = "4.20"
	cl.ControlPlaneIP = "172.16.0.20"

	st := cl.Status(t.TempDir())

	if st.Type != DistroOpenShift || st.OpenShiftVersion != "4.20" || st.K8sVersion != "" {
		t.Errorf("type/version = %s/%s/%s", st.Type, st.OpenShiftVersion, st.K8sVersion)
	}
	if st.CNI != "kindnet" {
		t.Errorf("cni = %q, want kindnet (MicroShift)", st.CNI)
	}
	if len(st.Nodes) != 1 || st.Nodes[0].Role != RoleControlPlane {
		t.Errorf("nodes = %+v, want the single control plane", st.Nodes)
	}
	if st.ControlPlaneIP != "172.16.0.20" {
		t.Errorf("control_plane_ip = %q, want the stored one", st.ControlPlaneIP)
	}
	if st.State != string(StateCreating) || st.AdminWorkstation != nil {
		t.Errorf("state = %q admin = %+v", st.State, st.AdminWorkstation)
	}
}
