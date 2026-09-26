# Development

## Building from Source

```bash
# Install Go 1.25+
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
│   ├── templates/            # HTML templates (HTMX + web/static/style.css)
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

The easiest way to set up both services is the installer's `--with-services` flag:

```bash
curl -fsSL https://raesene.github.io/baremetalvmm/install.sh | sudo bash -s -- --with-services
```

It installs `vmm.service` and `vmm-web.service`, enables both, generates a random web console password in `/etc/vmm-web/environment` (mode 600, printed once) and starts the console. On an existing install you can also run `sudo /usr/local/share/vmm/install-service.sh`, which installs the units and generates a password if none is set. It doesn't enable them.

### Auto-Start VMs on Boot

`vmm.service` is a oneshot unit: at boot it runs `vmm autostart` (starting VMs with `auto_start: true`, the default) and at shutdown `vmm autostop`, which stops every running VM.

```bash
sudo systemctl enable vmm
sudo systemctl status vmm
```

**Don't stop `vmm.service` to upgrade.** Stopping it runs `vmm autostop` and stops every running VM. Upgrades don't need it: `sudo vmm upgrade` swaps the binaries in place and only restarts `vmm-web`.

### Running vmm-web as a Service

```bash
sudo systemctl enable --now vmm-web
sudo systemctl status vmm-web
```

The password lives in `/etc/vmm-web/environment` (`VMM_WEB_PASSWORD=…`, at least 8 characters, common defaults rejected). The service listens on `127.0.0.1:8080`. To change the listen address, use a drop-in so the change survives upgrades:

```bash
sudo systemctl edit vmm-web
# [Service]
# ExecStart=
# ExecStart=/usr/local/bin/vmm-web --listen 0.0.0.0:8080
sudo systemctl restart vmm-web
```

The vmm-web service is ordered after `vmm.service`, so VMs will be started before the web UI comes up. `vmm upgrade` never overwrites installed unit files: if a release ships a changed unit it tells you, and `--update-units` applies it (keeping the old file as `.prev`).

## Installing and Upgrading

- `scripts/install.sh` is self-contained and is published on the project site, so `curl -fsSL https://raesene.github.io/baremetalvmm/install.sh | sudo bash` works without a checkout. Its `FC_VERSION` is the Firecracker version a release requires. `vmm upgrade` reads it from the release tarball's copy of `install.sh`.
- `vmm upgrade` (`cmd/vmm/upgrade.go`, `internal/upgrade/`) looks up the latest `v*` release, verifies the GoReleaser tarball against `checksums.txt`, and swaps binaries atomically (temp file + rename, old binary kept as `.prev`). It verifies that the new binary reports the expected version, and rolls back if it doesn't.
- Integration test against the real releases: `VMM_NETWORK_TESTS=1 go test ./internal/upgrade/`.

## AI Agent Skill

The `skills/vmm-usage/` directory contains a [Claude Code skill](https://docs.anthropic.com/en/docs/claude-code) that teaches AI agents how to use vmm. It covers VM lifecycle, SSH access, image management, and Kubernetes cluster creation. To use the skill, add it to your Claude Code configuration or reference it directly.
