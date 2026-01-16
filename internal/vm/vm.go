package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// State represents the current state of a VM
type State string

const (
	StateCreated  State = "created"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

// VM represents a microVM instance
type VM struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	State        State     `json:"state"`
	CPUs         int       `json:"cpus"`
	MemoryMB     int       `json:"memory_mb"`
	DiskSizeMB   int       `json:"disk_size_mb"`
	Image        string    `json:"image,omitempty"`
	KernelPath   string    `json:"kernel_path"`
	RootfsPath   string    `json:"rootfs_path"`
	IPAddress    string    `json:"ip_address"`
	TapDevice    string    `json:"tap_device"`
	MacAddress   string    `json:"mac_address"`
	SSHPort      int       `json:"ssh_port"`
	SSHPublicKey string    `json:"ssh_public_key,omitempty"`
	DNSServers   []string  `json:"dns_servers,omitempty"`
	SocketPath   string    `json:"socket_path"`
	PID          int       `json:"pid"`
	AutoStart    bool      `json:"auto_start"`
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	PortForwards []PortForward `json:"port_forwards,omitempty"`
}

// PortForward represents a port forwarding rule
type PortForward struct {
	HostPort  int    `json:"host_port"`
	GuestPort int    `json:"guest_port"`
	Protocol  string `json:"protocol"` // tcp or udp
}

// NewVM creates a new VM with default settings
func NewVM(name string) *VM {
	id := uuid.New().String()[:8]
	return &VM{
		ID:         id,
		Name:       name,
		State:      StateCreated,
		CPUs:       1,
		MemoryMB:   512,
		DiskSizeMB: 1024,
		AutoStart:  true,
		CreatedAt:  time.Now(),
	}
}

// GenerateMacAddress generates a MAC address based on the VM ID
func (v *VM) GenerateMacAddress() string {
	// Use AA:FC prefix (Firecracker convention) + parts of UUID
	return fmt.Sprintf("AA:FC:00:%02X:%02X:%02X",
		v.ID[0], v.ID[1], v.ID[2])
}

// Save persists the VM configuration to disk
func (v *VM) Save(vmDir string) error {
	path := filepath.Join(vmDir, v.Name+".json")
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal VM config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// Load reads a VM configuration from disk
func Load(vmDir, name string) (*VM, error) {
	path := filepath.Join(vmDir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read VM config: %w", err)
	}

	var vm VM
	if err := json.Unmarshal(data, &vm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VM config: %w", err)
	}
	return &vm, nil
}

// Delete removes the VM configuration from disk
func Delete(vmDir, name string) error {
	path := filepath.Join(vmDir, name+".json")
	return os.Remove(path)
}

// List returns all VMs in the given directory
func List(vmDir string) ([]*VM, error) {
	entries, err := os.ReadDir(vmDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*VM{}, nil
		}
		return nil, err
	}

	var vms []*VM
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := entry.Name()[:len(entry.Name())-5] // Remove .json extension
		vm, err := Load(vmDir, name)
		if err != nil {
			continue // Skip invalid configs
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// Exists checks if a VM with the given name exists
func Exists(vmDir, name string) bool {
	path := filepath.Join(vmDir, name+".json")
	_, err := os.Stat(path)
	return err == nil
}
