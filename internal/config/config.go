package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	DefaultDataDir    = "/var/lib/vmm"
	DefaultBridgeName = "vmm-br0"
	DefaultSubnet     = "172.16.0.0/16"
	DefaultGateway    = "172.16.0.1"
)

// Config holds the global VMM configuration
type Config struct {
	DataDir       string `json:"data_dir"`
	BridgeName    string `json:"bridge_name"`
	Subnet        string `json:"subnet"`
	Gateway       string `json:"gateway"`
	HostInterface string `json:"host_interface"`
	KernelPath    string `json:"kernel_path"`
	RootfsPath    string `json:"rootfs_path"`
}

// Paths returns commonly used paths derived from the config
type Paths struct {
	Config   string
	VMs      string
	Images   string
	Kernels  string
	Rootfs   string
	Sockets  string
	Logs     string
	State    string
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		DataDir:       DefaultDataDir,
		BridgeName:    DefaultBridgeName,
		Subnet:        DefaultSubnet,
		Gateway:       DefaultGateway,
		HostInterface: "eth0",
	}
}

// GetPaths returns all standard paths based on the data directory
func (c *Config) GetPaths() *Paths {
	return &Paths{
		Config:  filepath.Join(c.DataDir, "config"),
		VMs:     filepath.Join(c.DataDir, "vms"),
		Images:  filepath.Join(c.DataDir, "images"),
		Kernels: filepath.Join(c.DataDir, "images", "kernels"),
		Rootfs:  filepath.Join(c.DataDir, "images", "rootfs"),
		Sockets: filepath.Join(c.DataDir, "sockets"),
		Logs:    filepath.Join(c.DataDir, "logs"),
		State:   filepath.Join(c.DataDir, "state"),
	}
}

// EnsureDirectories creates all necessary directories
func (c *Config) EnsureDirectories() error {
	paths := c.GetPaths()
	dirs := []string{
		paths.Config,
		paths.VMs,
		paths.Kernels,
		paths.Rootfs,
		paths.Sockets,
		paths.Logs,
		paths.State,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	return nil
}

// Load reads the configuration from disk
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, err
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the configuration to disk
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ConfigPath returns the default config file path
func ConfigPath() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "vmm", "config.json")
	}

	// When running under sudo, use the original user's home directory
	home, _ := os.UserHomeDir()
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		home = filepath.Join("/home", sudoUser)
	}

	return filepath.Join(home, ".config", "vmm", "config.json")
}
