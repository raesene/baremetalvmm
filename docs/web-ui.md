# Web UI

VMM includes an optional web-based dashboard (`vmm-web`) for managing and monitoring VMs from a browser. It's a separate binary that reuses the same internal libraries as the CLI, so all operations are consistent between both interfaces.

## Starting the Web UI

The web UI requires a password set via the `VMM_WEB_PASSWORD` environment variable:

```bash
# Listen on localhost only (default)
VMM_WEB_PASSWORD=mysecretpassword sudo -E vmm-web

# Listen on all interfaces for remote access
VMM_WEB_PASSWORD=mysecretpassword sudo -E vmm-web --listen 0.0.0.0:8080
```

Then open `http://<host>:8080` in a browser and log in with username `admin` and the password you set.

## Features

- **Dashboard** - Overview of all VMs and clusters with resource usage stats
- **VM Management** - Create, start, stop, and delete VMs from the browser
- **Web Terminal** - Browser-based SSH terminal for running VMs (xterm.js + WebSocket)
- **Cluster Management** - Create and delete Kubernetes clusters
- **Live Status** - VM status updates via Server-Sent Events (no page refresh needed)
- **JSON API** - REST API at `/api/v1/` for scripting and automation
- **Authentication** - Session-based login with rate-limited password attempts

## Web Terminal

Running VMs have a **Terminal** button on their detail page that opens a full-screen browser terminal. The terminal connects via WebSocket to an SSH session on the VM, giving you interactive shell access without needing a local SSH client.

- Uses the host's SSH private key (auto-detected from `~/.ssh/`, same as `vmm ssh`)
- Supports terminal resize, scrollback, and clickable links
- Requires the VM to be in "running" state (the managed SSH key is always available)

## JSON API

The web UI exposes a JSON API for scripting. Authenticate by logging in via the browser to get a session token, then use it as a Bearer token:

```bash
# List VMs
curl -H "Authorization: Bearer <session-token>" http://localhost:8080/api/v1/vms

# Create a VM
curl -X POST -H "Authorization: Bearer <session-token>" \
  -H "Content-Type: application/json" \
  -d '{"name":"myvm","cpus":2,"memory_mb":1024}' \
  http://localhost:8080/api/v1/vms

# Start a VM
curl -X POST -H "Authorization: Bearer <session-token>" \
  http://localhost:8080/api/v1/vms/myvm/start

# Health check (no auth required)
curl http://localhost:8080/api/v1/health
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/health` | Health check (no auth) |
| GET | `/api/v1/vms` | List all VMs |
| POST | `/api/v1/vms` | Create a VM |
| GET | `/api/v1/vms/{name}` | Get VM details |
| POST | `/api/v1/vms/{name}/start` | Start a VM |
| POST | `/api/v1/vms/{name}/stop` | Stop a VM |
| DELETE | `/api/v1/vms/{name}` | Delete a VM |
| GET | `/api/v1/clusters` | List clusters |
| POST | `/api/v1/clusters` | Create a cluster |
| DELETE | `/api/v1/clusters/{name}` | Delete a cluster |

## SSH Key Behavior

When creating VMs or clusters via the API, VMM uses its auto-generated Ed25519 key pair (`/var/lib/vmm/ssh/vmm_ed25519`) for SSH access and cluster provisioning. No SSH key needs to be provided in API requests. If a user-provided SSH key is included, it is added alongside the managed key.

## Security

- **Default bind address** is `127.0.0.1:8080` (localhost only). You must explicitly pass `--listen 0.0.0.0:8080` to allow remote access.
- **Login rate limiting** - 5 attempts per minute per IP address.
- **Session cookies** are `HttpOnly` and `SameSite=Strict`.
- **CSRF protection** on all state-changing requests.
- **Security headers** - CSP, X-Frame-Options DENY, X-Content-Type-Options nosniff.
- The web server runs as root (required for Firecracker operations). For production use, consider putting it behind a reverse proxy with TLS (e.g., nginx, Caddy).
