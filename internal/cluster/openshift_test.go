package cluster

import "testing"

func TestK8sVersionForOpenShift(t *testing.T) {
	cases := map[string]string{
		"4.20": "1.33",
		"4.21": "1.34",
		"4.16": "1.29",
		"4.0":  "1.13",
		"bad":  "1.33", // fallback
		"":     "1.33", // fallback
	}
	for in, want := range cases {
		if got := k8sVersionForOpenShift(in); got != want {
			t.Errorf("k8sVersionForOpenShift(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCrioCandidateList(t *testing.T) {
	// For k8s 1.33 we expect a descending list centered on 1.33 so the newest
	// available CRI-O stable channel (1.32 at time of writing) is reachable.
	got := crioCandidateList("1.33")
	want := "v1.34 v1.33 v1.32 v1.31 v1.30"
	if got != want {
		t.Errorf("crioCandidateList(1.33) = %q, want %q", got, want)
	}
	// Malformed input falls back to the 1.33 default.
	if got := crioCandidateList("garbage"); got != want {
		t.Errorf("crioCandidateList(garbage) = %q, want %q", got, want)
	}
}

func TestNewClusterOpenShiftSingleNode(t *testing.T) {
	c := NewCluster("ocp", 3, "", DistroOpenShift)
	if c.Distro != DistroOpenShift {
		t.Errorf("Distro = %q, want %q", c.Distro, DistroOpenShift)
	}
	if len(c.WorkerVMs) != 0 {
		t.Errorf("OpenShift cluster should be single-node, got %d workers", len(c.WorkerVMs))
	}
	if len(c.ClusterVMs()) != 1 {
		t.Errorf("ClusterVMs() = %d, want 1", len(c.ClusterVMs()))
	}
}

func TestNewClusterDefaultsToKubeadm(t *testing.T) {
	c := NewCluster("k", 2, "1.36.0", "")
	if c.Distro != DistroKubeadm {
		t.Errorf("empty distro should default to kubeadm, got %q", c.Distro)
	}
	if len(c.WorkerVMs) != 2 {
		t.Errorf("kubeadm cluster should keep workers, got %d", len(c.WorkerVMs))
	}
}
