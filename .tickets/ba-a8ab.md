---
id: ba-a8ab
status: open
deps: [ba-1253]
links: []
created: 2026-01-29T18:11:43Z
type: task
priority: 2
assignee: vot3k
parent: ba-3d4c
---
# Create systemd service file and install script for vmm-api

## Objective
Create systemd service file and deployment script for vmm-api daemon.

## Location
- New file: scripts/vmm-api.service
- New file: scripts/install-api.sh

## Implementation Details

### Systemd Service File (scripts/vmm-api.service)
```ini
[Unit]
Description=VMM API Server
After=network.target
Documentation=https://github.com/raesene/baremetalvmm

[Service]
Type=simple
ExecStart=/usr/local/bin/vmm-api --listen :8443 --cert /etc/vmm/tls/server.crt --key /etc/vmm/tls/server.key --ca /etc/vmm/tls/ca.crt
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=vmm-api

# Security hardening
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=no
ReadWritePaths=/var/lib/vmm /etc/vmm

# Resource limits
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

Note: NoNewPrivileges=no because Firecracker operations require root.
ProtectHome=yes is OK because vmm-api doesn't need user home directories.
ReadWritePaths must include the data directory.

### Install Script (scripts/install-api.sh)
1. Check running as root
2. Copy vmm-api binary to /usr/local/bin/
3. Create /etc/vmm/tls/ directory
4. Copy systemd service file to /etc/systemd/system/
5. Reload systemd daemon
6. Print instructions for:
   - Generating TLS certificates
   - Placing certificates in /etc/vmm/tls/
   - Starting the service
   - Checking status

### Directory Setup
- /etc/vmm/ — configuration directory
- /etc/vmm/tls/ — TLS certificates
- /var/lib/vmm/ — data directory (already exists from vmm install)

## Acceptance Criteria
- vmm-api.service starts and restarts the daemon correctly
- Security directives restrict filesystem access
- Install script is idempotent (safe to run multiple times)
- Instructions printed after install are clear and complete

## Acceptance Criteria

systemctl start vmm-api works; service restarts on crash; install.sh is idempotent

