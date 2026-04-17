package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type State string

const (
	StateCreating State = "creating"
	StateRunning  State = "running"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

type Cluster struct {
	Name           string   `json:"name"`
	State          State    `json:"state"`
	K8sVersion     string   `json:"k8s_version"`
	ControlPlaneVM string   `json:"control_plane_vm"`
	WorkerVMs      []string `json:"worker_vms"`
	ControlPlaneIP string   `json:"control_plane_ip"`
	PodSubnet      string   `json:"pod_subnet"`
	ServiceSubnet  string   `json:"service_subnet"`
	JoinToken      string   `json:"join_token,omitempty"`
	JoinCAHash     string   `json:"join_ca_hash,omitempty"`
	CPUs           int      `json:"cpus"`
	MemoryMB       int      `json:"memory_mb"`
	DiskSizeMB     int      `json:"disk_size_mb"`
	SSHKeyPath     string   `json:"ssh_key_path"`
	Image          string   `json:"image,omitempty"`
	Kernel         string   `json:"kernel,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func NewCluster(name string, workers int, k8sVersion string) *Cluster {
	c := &Cluster{
		Name:           name,
		State:          StateCreating,
		K8sVersion:     k8sVersion,
		ControlPlaneVM: fmt.Sprintf("%s-control-plane", name),
		PodSubnet:      "10.244.0.0/16",
		ServiceSubnet:  "10.96.0.0/12",
		CPUs:           2,
		MemoryMB:       4096,
		DiskSizeMB:     10240,
		CreatedAt:      time.Now(),
	}
	for i := 1; i <= workers; i++ {
		c.WorkerVMs = append(c.WorkerVMs, fmt.Sprintf("%s-worker-%d", name, i))
	}
	return c
}

func (c *Cluster) AllVMs() []string {
	vms := []string{c.ControlPlaneVM}
	vms = append(vms, c.WorkerVMs...)
	return vms
}

func (c *Cluster) Save(clusterDir string) error {
	path := filepath.Join(clusterDir, c.Name+".json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cluster config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

func Load(clusterDir, name string) (*Cluster, error) {
	path := filepath.Join(clusterDir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read cluster config: %w", err)
	}
	var c Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cluster config: %w", err)
	}
	return &c, nil
}

func List(clusterDir string) ([]*Cluster, error) {
	entries, err := os.ReadDir(clusterDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Cluster{}, nil
		}
		return nil, err
	}
	var clusters []*Cluster
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := entry.Name()[:len(entry.Name())-5]
		c, err := Load(clusterDir, name)
		if err != nil {
			continue
		}
		clusters = append(clusters, c)
	}
	return clusters, nil
}

func Delete(clusterDir, name string) error {
	path := filepath.Join(clusterDir, name+".json")
	return os.Remove(path)
}

func Exists(clusterDir, name string) bool {
	path := filepath.Join(clusterDir, name+".json")
	_, err := os.Stat(path)
	return err == nil
}
