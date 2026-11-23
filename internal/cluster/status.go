package cluster

import (
	"time"

	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/vm"
)

// Node roles in Status. Callers pick nodes by these, so they are part of
// the machine-readable output and must not change.
const (
	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"
)

// StateDegraded is reported for a running cluster with a node or the admin
// workstation not running. It is never stored.
const StateDegraded = "degraded"

// VMStateMissing is a member VM's state when its VM record is gone.
const VMStateMissing = "missing"

// Status is a cluster as `vmm cluster list|show|create --json` print it.
// It is the stable machine-readable shape, so add fields rather than
// renaming them, and keep secrets (the kubeadm join token and CA hash) out.
type Status struct {
	Name             string     `json:"name"`
	State            string     `json:"state"`
	StatusMessage    string     `json:"status_message,omitempty"`
	Type             string     `json:"type"`
	CNI              string     `json:"cni,omitempty"`
	K8sVersion       string     `json:"k8s_version,omitempty"`
	OpenShiftVersion string     `json:"openshift_version,omitempty"`
	Nested           bool       `json:"nested"`
	ControlPlaneIP   string     `json:"control_plane_ip"`
	Context          string     `json:"context"`
	Nodes            []NodeStat `json:"nodes"`
	AdminWorkstation *MemberVM  `json:"admin_workstation,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// NodeStat is one Kubernetes node of a cluster.
type NodeStat struct {
	Role string `json:"role"`
	MemberVM
}

// MemberVM is a VM belonging to a cluster. IP is empty until the VM has one.
type MemberVM struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	State string `json:"state"`
}

// Context is the kubectl context vmm merges the cluster's kubeconfig as.
func (c *Cluster) Context() string {
	return "vmm-" + c.Name
}

// Status describes the cluster with the live state of its VMs, loaded from
// vmsDir.
func (c *Cluster) Status(vmsDir string) Status {
	member := func(name string) MemberVM {
		m := MemberVM{Name: name, State: VMStateMissing}
		if v, err := vm.Load(vmsDir, name); err == nil {
			hypervisor.UpdateVMState(v)
			m.IP, m.State = v.IPAddress, string(v.State)
		}
		return m
	}

	s := Status{
		Name:           c.Name,
		State:          string(c.State),
		StatusMessage:  c.StatusMessage,
		Type:           c.Distro,
		CNI:            c.CNI,
		Nested:         c.Nested,
		ControlPlaneIP: c.ControlPlaneIP,
		Context:        c.Context(),
		Nodes:          []NodeStat{},
		CreatedAt:      c.CreatedAt,
	}
	if s.Type == "" {
		s.Type = DistroKubeadm
	}
	if s.Type == DistroOpenShift {
		s.OpenShiftVersion = c.OpenShiftVer
		// MicroShift is always installed with its kindnet CNI; the stored
		// CNI is the kubeadm default and doesn't apply
		s.CNI = "kindnet"
	} else {
		s.K8sVersion = c.K8sVersion
		if s.CNI == "" {
			s.CNI = CNICilium
		}
	}

	allRunning := true
	add := func(role, name string) {
		m := member(name)
		allRunning = allRunning && m.State == string(vm.StateRunning)
		s.Nodes = append(s.Nodes, NodeStat{Role: role, MemberVM: m})
	}
	add(RoleControlPlane, c.ControlPlaneVM)
	for _, w := range c.WorkerVMs {
		add(RoleWorker, w)
	}
	if c.AdminVM != "" {
		m := member(c.AdminVM)
		allRunning = allRunning && m.State == string(vm.StateRunning)
		s.AdminWorkstation = &m
	}
	if s.ControlPlaneIP == "" {
		s.ControlPlaneIP = s.Nodes[0].IP
	}

	if c.State == StateRunning && !allRunning {
		s.State = StateDegraded
	}
	return s
}
