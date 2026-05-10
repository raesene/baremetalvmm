# Configuration

## Config File

VMM stores its configuration at `~/.config/vmm/config.json`. Initialize it with:

```bash
vmm config init
```

## Configurable VM Defaults

You can set default values for `vmm create` parameters in the config file under a `vm_defaults` section. This is useful if you typically use the same settings for most VMs.

### Available Default Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpus` | int | 1 | Number of vCPUs |
| `memory_mb` | int | 512 | Memory in MB |
| `disk_size_mb` | int | 1024 | Disk size in MB |
| `image` | string | (default rootfs) | Rootfs image name |
| `kernel` | string | (default kernel) | Kernel name |
| `ssh_key_path` | string | (none) | Path to SSH public key |
| `dns_servers` | []string | [8.8.8.8, 8.8.4.4, 1.1.1.1] | DNS servers |

### Example Configuration

```json
{
  "data_dir": "/var/lib/vmm",
  "bridge_name": "vmm-br0",
  "subnet": "172.16.0.0/16",
  "gateway": "172.16.0.1",
  "host_interface": "eth0",
  "vm_defaults": {
    "cpus": 2,
    "memory_mb": 1024,
    "disk_size_mb": 4096,
    "ssh_key_path": "~/.ssh/id_ed25519.pub",
    "kernel": "kernel-6.1",
    "dns_servers": ["9.9.9.9", "1.1.1.1"]
  }
}
```

### How Defaults Work

When you run `vmm create`, values are resolved in this order:
1. **CLI flag** - If you specify a flag (e.g., `--cpus 4`), it takes priority
2. **Config default** - If no flag is given, uses the value from `vm_defaults`
3. **Built-in default** - If neither is set, uses the built-in default

### Usage Examples

```bash
# With the example config above, this creates a VM with:
# - 2 CPUs, 1024 MB memory, 4096 MB disk (from config)
# - SSH key from ~/.ssh/id_ed25519.pub (from config)
# - kernel-6.1 kernel (from config)
sudo vmm create myvm

# Override specific defaults with CLI flags:
# - 4 CPUs (from flag), 1024 MB memory (from config)
sudo vmm create myvm --cpus 4

# Override multiple defaults:
sudo vmm create myvm --cpus 4 --memory 2048 --kernel kernel-5.10
```

### Viewing Current Defaults

```bash
vmm config show
# Output shows each setting and whether it comes from config or built-in default
```

The `vm_defaults` section is optional. Existing configs without it will continue to work unchanged, using the built-in defaults.

## Shell Completion

VMM supports shell completion for bash, zsh, and fish. Completions include command names, VM names, cluster names, kernel names, and image names.

```bash
# Bash (add to ~/.bashrc for persistence)
source <(vmm completion bash)

# Zsh (add to ~/.zshrc for persistence)
source <(vmm completion zsh)

# Fish
vmm completion fish | source
```
