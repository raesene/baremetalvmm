# Development

## Building from Source

```bash
# Install Go 1.21+
# Clone the repo
git clone https://github.com/raesene/baremetalvmm.git
cd baremetalvmm

# Build both binaries
make build-all

# Or build individually
go build -o vmm ./cmd/vmm/
go build -o vmm-web ./cmd/vmm-web/

# Run tests
go test ./...
```

## Architecture

```
+---------------------------+  +---------------------------+
|         vmm CLI           |  |     vmm-web (HTTP)        |
+---------------------------+  +---------------------------+
|  create | start | stop    |  |  Dashboard | VM mgmt      |
|  delete | list | ssh ...  |  |  Cluster mgmt | REST API  |
+-------------+-------------+  +-------------+-------------+
              |                               |
              +---------------+---------------+
                              v
+-------------------------------------------------------------+
|                  Internal Components                         |
+--------------+--------------+--------------+----------------+
|   Config     |   Network    |    Image     |  Firecracker   |
|   Store      |   Manager    |   Manager    |    Client      |
+--------------+--------------+--------------+----------------+
                              |
                              v
+-------------------------------------------------------------+
|                  Firecracker VMM                             |
|              (One process per microVM)                       |
+-------------------------------------------------------------+
```

## Project Structure

```
├── cmd/
│   ├── vmm/main.go           # CLI entry point
│   └── vmm-web/main.go       # Web UI entry point
├── internal/
│   ├── config/               # Configuration management
│   ├── vm/                   # VM struct and persistence
│   ├── cluster/              # Kubernetes cluster management
│   ├── firecracker/          # Firecracker SDK wrapper
│   ├── network/              # TAP/bridge networking
│   ├── image/                # Kernel/rootfs management
│   ├── mount/                # Host directory mount management
│   └── web/                  # Web UI server, handlers, auth
├── web/
│   ├── embed.go              # Go embed directive for assets
│   ├── templates/            # HTML templates (HTMX + Tailwind)
│   └── static/               # JS/CSS assets (htmx, sse, styles)
├── scripts/
│   ├── install.sh            # Installation script
│   ├── uninstall.sh          # Uninstallation script
│   ├── install-service.sh    # Systemd service installation (optional)
│   ├── build-kernel.sh       # Custom kernel build script
│   ├── build-rootfs.sh       # Custom rootfs build script
│   ├── vmm.service           # Systemd service for VM auto-start
│   └── vmm-web.service       # Systemd service for web UI
└── go.mod                    # Go modules
```

## Directory Structure (Runtime)

```
/var/lib/vmm/
├── config/           # Global configuration
├── vms/              # VM configurations and rootfs
├── clusters/         # Cluster configurations (JSON)
├── images/
│   ├── kernels/      # Linux kernel images
│   └── rootfs/       # Root filesystem images
├── mounts/           # Mount images (ext4 images from host directories)
├── sockets/          # Firecracker API sockets
├── logs/             # VM logs
└── state/            # Runtime state
```

## Systemd Services

### Auto-Start VMs on Boot

```bash
# Install systemd services (vmm + vmm-web if binary is present)
sudo ./scripts/install-service.sh

# Enable VM auto-start
sudo systemctl enable vmm

# Check status
sudo systemctl status vmm
```

VMs with `auto_start: true` (the default) will be started automatically.

### Running vmm-web as a Service

The install script also sets up a systemd service for the web UI. The password is stored in `/etc/vmm-web/environment` (created automatically with mode 600):

```bash
# Set the web UI password
sudo nano /etc/vmm-web/environment
# Change: VMM_WEB_PASSWORD=changeme

# Enable and start the web UI
sudo systemctl enable vmm-web
sudo systemctl start vmm-web

# Check status
sudo systemctl status vmm-web
```

The service listens on `0.0.0.0:8080` by default. To change the listen address, edit the `ExecStart` line in `/etc/systemd/system/vmm-web.service` and run `sudo systemctl daemon-reload`.

The vmm-web service is ordered after `vmm.service`, so VMs will be started before the web UI comes up.

## AI Agent Skill

The `skills/vmm-usage/` directory contains a [Claude Code skill](https://docs.anthropic.com/en/docs/claude-code) that teaches AI agents how to use vmm. It covers VM lifecycle, SSH access, image management, and Kubernetes cluster creation. To use the skill, add it to your Claude Code configuration or reference it directly.
