# Codex Code Review

Review date: 2026-05-25

Scope: code quality, architecture, and security review of the current working tree. I did not run builds or tests because this project needs Linux/KVM/Firecracker support and the user asked to avoid building. I did run read-only repository inspection and `gofmt -l`.

## Executive Summary

This is a useful development-focused Firecracker manager, but it currently treats many user-controlled strings as trusted identifiers. Because the CLI and web UI run privileged operations as root, this turns simple missing validation into host filesystem writes/deletes, stale network rules, guest command injection, and difficult-to-debug state corruption.

The highest-value improvement is to introduce a small, shared internal service layer for VM/cluster/image operations. That layer should own validation, path confinement, locking, atomic persistence, lifecycle rollback, and network reconciliation, and both `cmd/vmm` and `internal/web` should call it instead of duplicating lifecycle code.

Priority order for another coding agent:

1. Add centralized identifier validation and safe path joining.
2. Tighten runtime data permissions and atomic, locked state writes.
3. Add lifecycle cleanup/rollback and reconcile commands for TAP/iptables/socket state.
4. Harden `vmm-web`: timeouts, origin checks, rate limiting, stronger cookies, no default password.
5. Add supply-chain verification for downloaded binaries, kernels, and rootfs images.
6. Consolidate duplicated CLI/web VM start/stop/delete flows into shared services.
7. Add tests around validation, state locking, path confinement, network rule generation, and web auth.

## Findings

### High: Untrusted names can escape runtime directories

Evidence:

- VM config paths are built from `v.Name`/`name` without validation in `internal/vm/vm.go:90`, `internal/vm/vm.go:100`, and `internal/vm/vm.go:115`.
- Cluster config paths use the same pattern in `internal/cluster/cluster.go:71`, `internal/cluster/cluster.go:80`, and `internal/cluster/cluster.go:116`.
- Image and kernel paths are also built from unchecked names in `internal/image/image.go:358`, `internal/image/image.go:369`, `internal/image/image.go:1227`, `internal/image/image.go:1236`, and `internal/image/image.go:1282`.
- CLI and web entry points accept names directly, for example `cmd/vmm/main.go:86`, `cmd/vmm/main.go:912`, `cmd/vmm/main.go:1053`, `internal/web/handlers_vm.go:87`, `internal/web/handlers_vm.go:496`, and `internal/web/handlers_images.go:84`.

Impact:

An authenticated web user, API client, or root CLI invocation can create names such as `../../../../tmp/foo` and cause reads, writes, deletes, sockets, logs, or imported kernels/images outside the intended `/var/lib/vmm` subtree. Because the web service is intended to run as root, this is the project-level security issue to fix first.

Implementation task:

- Add `internal/validate` with functions like `Identifier(kind, value string) error`.
- Allow a conservative pattern such as `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`.
- Reject empty strings, `.`, `..`, path separators, control characters, leading dashes, and names reserved by the app.
- Add a `safejoin` helper that cleans and joins paths, then verifies the result remains under the intended base directory.
- Apply this to VM names, cluster names, image names, kernel names, downloaded release local names, mount tags, and any route/path parameter.
- Refuse to load existing configs with invalid names except through a migration or repair command.

Tests to add:

- Table tests showing `../x`, `/x`, `x/y`, `x\ny`, `.`, `..`, and very long names fail for every entity type.
- Tests proving `Save`, `Load`, `Delete`, `GetImagePath`, `GetKernelPath`, and release download paths cannot leave their base directories.
- HTTP tests for VM, cluster, image, and kernel endpoints returning `400` on invalid names.

### High: Runtime state files and cluster secrets are world-readable by default

Evidence:

- All runtime directories are created with `0755` in `internal/config/config.go:119` to `internal/config/config.go:138`.
- VM configs are written with `0644` in `internal/vm/vm.go:90` to `internal/vm/vm.go:96`.
- Cluster configs are written with `0644` in `internal/cluster/cluster.go:71` to `internal/cluster/cluster.go:77`.
- Cluster configs include Kubernetes join material and SSH key paths in `internal/cluster/cluster.go:30` to `internal/cluster/cluster.go:35`.

Impact:

Any local user can list runtime state, VM names, socket/log paths, rootfs paths, SSH public keys, cluster node names, SSH private key paths, and Kubernetes join tokens. That is not acceptable for a root-operated control plane, especially if clusters are reachable from the host.

Implementation task:

- Set a process umask during CLI and web startup, or explicitly use restrictive modes.
- Use `0700` for `config`, `vms`, `clusters`, `sockets`, `state`, and `ssh`.
- Use `0750` or `0755` only for image directories if non-root users genuinely need read-only listing.
- Write VM and cluster configs with `0600`.
- Add a one-time permissions repair command or run repair from `config init`.
- Avoid storing Kubernetes join token after provisioning if it is not needed later. If needed, store it encrypted or at least `0600`.

Tests to add:

- Unit tests around `EnsureDirectories` mode expectations.
- Persistence tests checking VM and cluster files are `0600`.

### High: Downloaded binaries, kernels, and rootfs images are not verified

Evidence:

- `internal/image/image.go:553`, `internal/image/image.go:567`, `internal/image/image.go:575`, `internal/image/image.go:1227`, and `internal/image/image.go:1236` download executable kernels and root filesystems without checksums or signatures.
- `scripts/install.sh:96` to `scripts/install.sh:116` downloads and extracts release binaries as root.
- `scripts/install.sh:200` to `scripts/install.sh:215` downloads Firecracker as root.
- Kernel build downloads source and config from live URLs in `scripts/build-kernel.sh:153` to `scripts/build-kernel.sh:166` and `scripts/build-kernel.sh:177` to `scripts/build-kernel.sh:187`.

Impact:

HTTPS alone does not provide release integrity or provenance. A compromised release, GitHub token, CDN path, DNS/TLS endpoint, or local `/tmp` race can install root-executed code or boot untrusted guest kernels/rootfs images.

Implementation task:

- Publish SHA256 checksums for every release asset and verify them before use.
- Prefer signed checksum manifests with minisign/cosign/GitHub artifact attestation.
- Pin Firecracker asset checksums for the supported version.
- Replace `/tmp/vmm.tar.gz` and `/tmp/firecracker.tgz` with `mktemp -d` and trap cleanup.
- Add max download sizes and gzip decompression limits in `internal/image`.
- In the web UI, do not accept arbitrary URL/local-name pairs from forms. Submit a release tag/type and re-resolve it server-side from an allowlisted release list.

Tests to add:

- Unit tests for checksum mismatch, oversized download, and gzip bomb rejection using `httptest.Server`.
- Script-level shellcheck or bats tests for `mktemp` cleanup paths.

### High: VM and cluster state writes are not atomic or locked

Evidence:

- VM state uses direct `os.WriteFile` in `internal/vm/vm.go:90` to `internal/vm/vm.go:96`.
- Cluster state uses direct `os.WriteFile` in `internal/cluster/cluster.go:71` to `internal/cluster/cluster.go:77`.
- Start flows allocate IPs from a snapshot of current JSON state and then save later in `cmd/vmm/main.go:531` to `cmd/vmm/main.go:540` and `internal/web/handlers_vm.go:254` to `internal/web/handlers_vm.go:267`.
- `vm.List` silently skips unreadable or invalid configs in `internal/vm/vm.go:130` to `internal/vm/vm.go:140`.

Impact:

Concurrent CLI/web starts can allocate duplicate IPs, corrupt JSON, lose state transitions, or hide broken configs. This is likely once the web UI can start multiple VMs or a systemd autostart overlaps manual operations.

Implementation task:

- Add `internal/store` with a host-level lock file, for example `/var/lib/vmm/state/lock`.
- Use `flock` or `golang.org/x/sys/unix.Flock` around every state-changing operation.
- Write JSON atomically: temp file in same directory, fsync file, chmod/chown, rename, fsync directory.
- Make list operations report invalid configs as warnings/errors rather than silently hiding them.
- Treat IP allocation, TAP creation, port-forward setup, Firecracker start, and state save as one locked operation or use a reservation record.

Tests to add:

- Concurrent start/allocation tests using temp dirs.
- Atomic write tests that simulate partial writes.
- List tests that surface invalid JSON.

### High: Firecracker is launched without jailer or host-side isolation hardening

Evidence:

- `internal/firecracker/client.go:157` to `internal/firecracker/client.go:163` starts Firecracker through the SDK process runner without a jailer config, dedicated UID/GID, chroot, cgroup, or per-VM resource boundary.
- `PLAN.md` explicitly lists jailer integration and resource quotas as not implemented.

Impact:

The project goal is stronger isolation than containers, and the docs include a security-testing kernel/rootfs. Running the VMM process as root without Firecracker jailer integration reduces defense in depth if Firecracker, KVM, the host kernel, or a block image path is attacked.

Implementation task:

- Add optional jailer support first, then make it the default for `vmm-web` and normal VM starts.
- Create per-VM jail directories under a root-owned runtime directory.
- Run Firecracker under a dedicated unprivileged UID/GID per VM or a shared `vmm-firecracker` user.
- Add cgroup limits for CPU, memory, pids, and block IO where practical.
- Document which capabilities are still needed by the privileged manager versus the Firecracker child process.

Tests to add:

- Unit tests for jail path generation and UID/GID selection.
- Integration test gated behind KVM that asserts the Firecracker process is not running as root when jailer mode is enabled.

### High: Web terminal disables origin checks

Evidence:

- `internal/web/handlers_terminal.go:89` to `internal/web/handlers_terminal.go:91` accepts the WebSocket with `InsecureSkipVerify: true`.
- The terminal route is a GET route in `internal/web/server.go:138` to `internal/web/server.go:140`; CSRF checks skip GET requests in `internal/web/auth.go:153` to `internal/web/auth.go:158`.

Impact:

The terminal bridges a browser WebSocket to root SSH inside a VM. Disabling origin verification increases the risk of cross-site WebSocket hijacking or same-site abuse if a browser has a valid session.

Implementation task:

- Remove `InsecureSkipVerify`.
- Add an explicit origin check allowing only the configured host and trusted reverse-proxy origins.
- Require a per-session terminal nonce in the WebSocket URL or subprotocol, and bind it to the current session token.
- Expire terminal nonces quickly and single-use them.

Tests to add:

- WebSocket test with a hostile `Origin` header returning forbidden.
- WebSocket test with missing/expired terminal nonce returning forbidden.
- Positive same-origin test.

### Medium-High: Login rate limiting and password handling are weak

Evidence:

- The login limiter keys on `r.RemoteAddr` in `internal/web/auth.go:82` to `internal/web/auth.go:84`. Without trusted proxy handling this may include a source port, and `middleware.RealIP` is enabled globally in `internal/web/server.go:106` to `internal/web/server.go:109`.
- Session and API tokens ignore `rand.Read` errors in `internal/web/auth.go:21` to `internal/web/auth.go:24` and `internal/web/server.go:33` to `internal/web/server.go:45`.
- `scripts/install-service.sh:35` to `scripts/install-service.sh:38` creates a default `VMM_WEB_PASSWORD=changeme`.
- Cookies are not marked `Secure` in `internal/web/auth.go:99` to `internal/web/auth.go:107`.

Impact:

Remote deployments can have bypassable login throttling, weak/default credentials, and cleartext session cookies if exposed without TLS. The web UI controls root host operations, so these defaults should be stricter than a normal development dashboard.

Implementation task:

- Normalize client IP with `net.SplitHostPort`.
- Only trust `X-Forwarded-For` / `X-Real-IP` when the remote peer is in an explicit trusted proxy list.
- Refuse `changeme` and short/common passwords at startup.
- Handle CSPRNG failures and fail closed.
- Set `Secure` cookies when TLS is enabled or when `--cookie-secure` is set; document reverse-proxy requirements.
- Consider replacing password-only auth with local Unix socket auth, mTLS, or OIDC for remote use.

Tests to add:

- Rate-limiter tests proving multiple ports from the same IP share one bucket.
- Startup tests rejecting empty/default/weak passwords.
- Cookie attribute tests for secure mode.

### Medium-High: Lifecycle operations are not transactional and leak host resources on failure

Evidence:

- CLI start creates or reuses TAP devices before Firecracker start, then only sets `StateError` on failure in `cmd/vmm/main.go:524` to `cmd/vmm/main.go:562`.
- Web start installs port-forward rules before Firecracker start in `internal/web/handlers_vm.go:260` to `internal/web/handlers_vm.go:288`.
- Stop/delete code tries cleanup but does not consistently check or retry failures, for example `internal/web/handlers_vm.go:360` to `internal/web/handlers_vm.go:374` and `internal/web/handlers_vm.go:415` to `internal/web/handlers_vm.go:429`.

Impact:

Failed starts can leave TAP devices, port-forward rules, sockets, and partially prepared rootfs images behind. Later starts can collide with stale state or accidentally expose ports to an old/new IP.

Implementation task:

- Build a cleanup stack in the shared VM service: rootfs created, SSH/DNS injected, mount images created, TAP created, iptables rules added, socket created.
- On failure, unwind in reverse order unless a resource already existed before the operation.
- Add a `vmm doctor` or `vmm reconcile` command that compares JSON state against actual TAP, socket, process, and iptables state and repairs stale resources.
- Persist port-forward state only after successful rule installation, and remove rules if the VM fails to start.

Tests to add:

- Mock command runner tests proving TAP and iptables cleanup happens after injected failures.
- Reconcile tests for stale TAP/socket/rule cases.

### Medium-High: PID-based fallback can kill unrelated processes

Evidence:

- Force delete and stop fall back to `SIGKILL` using a PID stored in JSON in `cmd/vmm/main.go:294` to `cmd/vmm/main.go:310`, `cmd/vmm/main.go:611` to `cmd/vmm/main.go:615`, `internal/web/handlers_vm.go:349` to `internal/web/handlers_vm.go:355`, and `internal/web/handlers_vm.go:404` to `internal/web/handlers_vm.go:410`.
- `IsRunning` treats a stale socket as running when PID is zero in `internal/firecracker/client.go:223` to `internal/firecracker/client.go:247`.

Impact:

If a PID is stale, reused, or corrupted, `vmm` can kill a process that is not the Firecracker VM. A stale socket can also cause incorrect state reporting.

Implementation task:

- Persist process start time or a pidfd where available.
- Before signalling, verify `/proc/<pid>/exe` or `/proc/<pid>/cmdline` matches Firecracker and the expected socket path.
- Prefer SDK/API shutdown and pidfd signalling over raw PID reuse.
- Treat `pid == 0` plus socket file as indeterminate, not definitely running.

Tests to add:

- Unit tests for process verification parsing.
- State tests where stale socket with zero PID becomes stopped/error instead of running.

### Medium: CLI and web VM lifecycle implementations diverge

Evidence:

- CLI start handles host directory mount images, fstab injection, and extra Firecracker drives in `cmd/vmm/main.go:474` to `cmd/vmm/main.go:508` and passes `MountDrives` in `cmd/vmm/main.go:544` to `cmd/vmm/main.go:556`.
- Web `startVM` does not handle mount images or `MountDrives` in `internal/web/handlers_vm.go:213` to `internal/web/handlers_vm.go:295`.
- Cluster start has another separate implementation in `cmd/vmm/main.go:1872` to `cmd/vmm/main.go:1954`.

Impact:

A VM created with mounts through the CLI will not be started equivalently from the web UI. More generally, bug fixes in one lifecycle path can be missed in the other two.

Implementation task:

- Introduce `internal/runtime` or `internal/service` with `CreateVM`, `StartVM`, `StopVM`, `DeleteVM`, `CreateCluster`, and `DeleteCluster`.
- Make CLI, web, autostart, and cluster paths call this service.
- Inject dependencies such as command runner, image manager, network manager, and Firecracker client so unit tests can mock host operations.

Tests to add:

- One service test for VM start with mounts asserting drives and fstab entries are built.
- CLI/web handler tests that only verify they call the service correctly.

### Medium: Network configuration claims configurability but hardcodes `/16`

Evidence:

- Bridge setup uses `m.Gateway + "/16"` rather than the configured subnet prefix in `internal/network/network.go:37` to `internal/network/network.go:39`.
- IP allocation manually edits the third and fourth IPv4 bytes and scans up to a `/16` in `internal/network/network.go:90` to `internal/network/network.go:124`.
- Kernel args hardcode `255.255.0.0` in `internal/firecracker/client.go:82` to `internal/firecracker/client.go:86`.
- Uninstall tries to delete a different NAT rule than the app creates: setup uses `! -o vmm-br0` in `internal/network/network.go:149` to `internal/network/network.go:154`, while uninstall checks `-o "$HOST_IFACE"` in `scripts/uninstall.sh:157` to `scripts/uninstall.sh:160`.

Impact:

Changing `subnet` in the config will not work reliably. The uninstaller can leave NAT rules behind, and custom networking may silently misconfigure guests.

Implementation task:

- Parse `cfg.Subnet` with `net/netip`.
- Derive bridge address prefix and guest netmask from the configured CIDR.
- Allocate addresses by iterating the actual prefix range and reserving network, broadcast, and gateway addresses.
- Generate matching add/delete iptables rules from a single source of truth.
- Consider creating a dedicated `VMM-POSTROUTING`/`VMM-PREROUTING` chain so cleanup is deterministic.

Tests to add:

- Address allocation tests for `/24`, `/20`, and `/16`.
- Kernel arg netmask tests.
- iptables add/delete command generation tests.

### Medium: Port-forwarding is not idempotent and is exposed broadly

Evidence:

- `AddPortForward` always appends a PREROUTING rule in `internal/network/network.go:127` to `internal/network/network.go:136`.
- CLI port parsing does not validate port ranges in `cmd/vmm/main.go:1195` to `cmd/vmm/main.go:1201`.
- There is no host bind address in `vm.PortForward` at `internal/vm/vm.go:52` to `internal/vm/vm.go:57`.

Impact:

Duplicate rules can accumulate, invalid ports are left to iptables errors, and every forwarded port is exposed on all host interfaces. That may surprise users because the default networking story says VMs are local-only until forwarding is configured.

Implementation task:

- Validate protocol and port ranges before calling iptables.
- Check for existing rules with `iptables -C` before appending.
- Add host bind address support, defaulting to `127.0.0.1` for safer development use, with explicit `0.0.0.0` for external exposure.
- Add OUTPUT-chain handling if host-local access to forwarded ports is intended.
- Store enough metadata to remove exactly the rules that were added.

Tests to add:

- Duplicate add tests.
- Invalid port/protocol tests.
- Rule rendering tests for localhost-only and all-interface forwards.

### Medium: Web image download endpoints trust client-supplied URLs

Evidence:

- Kernel downloads read `url` and `local_name` from the form in `internal/web/handlers_images.go:84` to `internal/web/handlers_images.go:101`.
- Rootfs downloads do the same in `internal/web/handlers_images.go:104` to `internal/web/handlers_images.go:121`.

Impact:

Even with authentication, a root web process should not let the browser choose arbitrary download URLs and filesystem names. This creates an SSRF primitive and compounds the path traversal issue.

Implementation task:

- Change forms to submit a release ID or `(type, tag, asset)` tuple.
- On POST, call `ListAvailableReleases`, find an exact match, and use only the server-derived URL and local name.
- Restrict release hosts to `github.com`/`objects.githubusercontent.com` or use the GitHub API asset ID.

Tests to add:

- POST with a tampered URL is rejected.
- POST with a tampered local name is rejected.
- Valid release ID resolves and downloads through the image manager.

### Medium: Kubernetes provisioning builds shell commands from unquoted input

Evidence:

- Hostname and `/etc/hosts` updates interpolate node names in `internal/cluster/provisioner.go:193` to `internal/cluster/provisioner.go:196`.
- `kubeadm init` interpolates version, subnets, IP, and node name in `internal/cluster/provisioner.go:204` to `internal/cluster/provisioner.go:207`.
- Worker join interpolates token/hash/node names in `internal/cluster/provisioner.go:254` to `internal/cluster/provisioner.go:260`.
- Web/API cluster creation accepts names and versions without strict validation in `internal/web/handlers_cluster.go:77` to `internal/web/handlers_cluster.go:147` and `internal/web/handlers_cluster.go:346` to `internal/web/handlers_cluster.go:403`.

Impact:

Most execution is inside the guest VM, not directly on the host, but malformed names or Kubernetes versions can execute arbitrary root shell in the guest, break provisioning, or poison kubeconfig entries. If a future mount/shared-secret feature is added, this becomes more serious.

Implementation task:

- Reuse identifier validation for cluster and node names.
- Validate Kubernetes versions with a strict semver pattern.
- Use shell quoting for every interpolated value or run commands through a structured script with environment variables.
- Avoid carrying raw join tokens longer than needed.

Tests to add:

- Version validation tests.
- Command construction tests with malicious names such as `x; touch /tmp/pwn`.

### Medium: HTTP server lacks production safety defaults

Evidence:

- `vmm-web` starts with `http.ListenAndServe` in `internal/web/server.go:213` to `internal/web/server.go:216`.
- No read, read-header, write, idle, or shutdown timeouts are configured.
- Systemd units in `scripts/vmm.service:6` to `scripts/vmm.service:10` and `scripts/vmm-web.service:6` to `scripts/vmm-web.service:10` have no hardening directives.

Impact:

If exposed remotely, slow clients can tie up the root web process. The systemd services also run with broad root privileges and default filesystem/process visibility.

Implementation task:

- Replace `http.ListenAndServe` with an `http.Server` using `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`.
- Add graceful shutdown handling for SIGINT/SIGTERM.
- Harden systemd where compatible: `NoNewPrivileges=true`, `PrivateTmp=true`, `ProtectSystem=strict`, `ProtectHome=read-only`, `ReadWritePaths=/var/lib/vmm /etc/vmm-web`, `CapabilityBoundingSet=...`, and `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`.
- Split privileged host operations into a smaller root helper or daemon if possible, allowing the web UI to run unprivileged.

Tests to add:

- Unit test for server timeout configuration.
- Manual systemd smoke test on Ubuntu 24.04.

### Medium: CSRF logic is coupled to the session token and skips on any bearer-looking header

Evidence:

- Rendered pages expose the session token as `CSRFToken` in `internal/web/server.go:232` to `internal/web/server.go:237`.
- CSRF validation only checks whether the submitted token is any valid session token, not whether it matches the current cookie, in `internal/web/auth.go:166` to `internal/web/auth.go:184`.
- CSRF is skipped whenever an `Authorization: Bearer ...` header exists in `internal/web/auth.go:160` to `internal/web/auth.go:164`.

Impact:

SameSite cookies reduce the practical browser CSRF risk, but the implementation is brittle and makes the session token double as a CSRF secret. A request authenticated by cookie but carrying an invalid bearer-looking header bypasses CSRF checks.

Implementation task:

- Generate a separate CSRF token per session and store it alongside the session expiry.
- Require the submitted token to match the current session cookie's CSRF token.
- Skip CSRF only when authentication actually succeeded via a valid bearer token, not merely when a header is present.

Tests to add:

- Cookie-authenticated POST with no CSRF returns `403`.
- Cookie-authenticated POST with another session's CSRF returns `403`.
- Cookie-authenticated POST with invalid bearer header still requires CSRF.
- Valid bearer-authenticated API POST skips CSRF.

### Medium: Resource values and DNS values need server-side validation

Evidence:

- CLI create accepts `--cpus`, `--memory`, and `--disk` without upper/lower bound checks in `cmd/vmm/main.go:177` to `cmd/vmm/main.go:187`.
- Web form parsing defaults invalid values but does not cap large values in `internal/web/handlers_vm.go:113` to `internal/web/handlers_vm.go:120`.
- DNS values are written directly into guest `resolv.conf` in `internal/image/image.go:953` to `internal/image/image.go:963`.

Impact:

Invalid or excessive resources can fail late or consume host disk. DNS strings containing whitespace/newlines can inject arbitrary guest `resolv.conf` lines.

Implementation task:

- Add validation for CPU, memory, disk, workers, Kubernetes version, DNS servers, port forwards, image/kernel names, and SSH public keys.
- Define sane maximums in config, for example max disk size and max memory.
- Validate DNS servers with `netip.ParseAddr` or explicitly allow resolver names only if supported.

Tests to add:

- Resource boundary tests.
- DNS newline/injection tests.
- Web API invalid resource tests.

### Medium: Install/uninstall scripts are not idempotent enough for host cleanup

Evidence:

- Install writes predictable temp files in `/tmp` in `scripts/install.sh:99` to `scripts/install.sh:109` and `scripts/install.sh:207` to `scripts/install.sh:214`.
- Uninstall does not remove `vmm-web.service` or `/etc/vmm-web/environment` in `scripts/uninstall.sh:212` to `scripts/uninstall.sh:220`.
- Uninstall NAT cleanup does not match the rule created by the Go code, as noted above.

Impact:

Install can be affected by local temp-file races when run as root. Uninstall can leave services, credentials, or firewall rules behind.

Implementation task:

- Use `mktemp -d` for all install downloads and extraction.
- Remove both `vmm.service` and `vmm-web.service`.
- Remove or prompt for `/etc/vmm-web`.
- Share network cleanup logic with the Go network manager or generate rules from the same constants.
- Add a `--dry-run` mode to uninstall for safer review.

Tests to add:

- Shellcheck.
- Bats tests for service cleanup and temp directory usage where practical.

### Low-Medium: `gofmt` is not clean

Evidence:

`gofmt -l cmd internal web` reported:

- `cmd/vmm/main.go`
- `internal/cluster/cluster.go`
- `internal/cluster/kubeconfig.go`
- `internal/config/config.go`
- `internal/firecracker/client.go`
- `internal/vm/vm.go`
- `internal/web/handlers_terminal.go`

Impact:

Formatting drift makes reviews noisier and suggests CI is not enforcing basic Go hygiene.

Implementation task:

- Run `gofmt -w` on Go sources.
- Add `gofmt -l` or `gofmt -w && git diff --exit-code` to CI.

Tests to add:

- CI formatting check.

### Low-Medium: There are no Go tests in the repository

Evidence:

- No `_test.go` files were found.
- The Makefile has a `test` target in `Makefile:38` to `Makefile:40`, but there is no test suite to run.

Impact:

The riskiest behavior is deterministic and unit-testable: name validation, safe path joins, state persistence, IP allocation, iptables command rendering, CSRF decisions, and command construction. Lack of tests makes changes to privileged host behavior much riskier.

Implementation task:

- Start with pure unit tests around validation and path confinement.
- Add mock command runners for network, mount, image, and Firecracker operations.
- Add HTTP handler tests with `httptest`.
- Gate true Firecracker/KVM integration tests behind build tags such as `integration` and environment checks.

Suggested first tests:

- `internal/validate` table tests.
- `internal/network` IP allocation and rule rendering tests.
- `internal/web` auth/CSRF/rate-limit tests.
- `internal/vm` and `internal/cluster` atomic persistence tests.

### Low: Documentation and config expectations are inconsistent

Evidence:

- README says Go 1.21+ in `README.md:18`, while `go.mod:3` says `go 1.25.3` and release CI uses Go 1.25 in `.github/workflows/release.yaml:20` to `.github/workflows/release.yaml:24`.
- `docs/development.md` says the installed `vmm-web` service listens on `0.0.0.0:8080`, while `scripts/vmm-web.service:9` uses `127.0.0.1:8080`.
- The web API cluster create endpoint requires `ssh_key` and `ssh_key_path` in `internal/web/handlers_cluster.go:368` to `internal/web/handlers_cluster.go:375`, but the CLI and docs describe VMM-managed SSH keys as optional/default.

Impact:

Users and agents will make wrong assumptions about supported Go versions, service exposure, and API requirements.

Implementation task:

- Pick the real minimum Go version and align README, CLAUDE.md, workflows, and `go.mod`.
- Align web service docs with the actual systemd unit.
- Make API cluster creation support the same VMM-managed key behavior as CLI/web form creation, or document the difference explicitly.

### Low: CSP and template behavior are inconsistent

Evidence:

- CSP disallows inline scripts in `internal/web/server.go:196` to `internal/web/server.go:202`.
- `web/templates/api_key.html:13` uses an inline `onclick`.
- `web/templates/vm_terminal.html:9` to `web/templates/vm_terminal.html:20` and `web/templates/vm_terminal.html:24` use inline styles. Inline styles are currently allowed by CSP, but scripts are not.

Impact:

The API key Copy button is likely blocked by CSP, and future inline handlers will fail silently.

Implementation task:

- Replace inline `onclick` with a `data-action` or element ID handler in `web/static/app.js`.
- Keep CSP as strict as practical and avoid adding `'unsafe-inline'` for scripts.
- Consider moving terminal inline styles into `web/static/style.css`.

Tests to add:

- A lightweight browser or Playwright check that the API key copy button attaches a listener and no CSP errors occur.

## Architecture Recommendations

### Introduce Shared Services

Create a small service layer under `internal/service` or `internal/runtime`:

- `VMService.Create`
- `VMService.Start`
- `VMService.Stop`
- `VMService.Delete`
- `VMService.Reconcile`
- `ImageService.Import/Download/Delete`
- `ClusterService.Create/Delete/Provision`

These services should depend on interfaces for command execution, Firecracker, image management, and networking. The CLI and web handlers should become thin adapters for parsing input and rendering output.

### Model Host Resources Explicitly

Today the persisted VM state is both desired state and observed runtime state. Split them:

- Desired config: CPU, memory, disk, image, kernel, mounts, port forwards, autostart.
- Runtime state: assigned IP, PID, socket path, TAP name, started time, last error.
- Observed state: derived from process/socket/network inspection during list/reconcile.

This makes recovery, failed starts, and concurrent operations easier to reason about.

### Add a Security Profile

Document and enforce different modes:

- Development localhost mode: root web service on 127.0.0.1, password auth acceptable.
- Remote admin mode: TLS/reverse proxy, secure cookies, stronger auth, origin checks.
- Security testing mode: jailer required, explicit warning if vulnerable kernels/rootfs are used without extra host isolation.

### Prefer nftables or Dedicated Chains

Using raw iptables append/delete calls spread through lifecycle code is fragile. A dedicated chain per VMM or per VM gives safer idempotency and cleanup. If Ubuntu 24.04 is the reference platform, consider nftables directly or at least detect iptables-nft behavior.

## Verification Not Run

Per user request, I did not run `go test`, `go build`, Firecracker, mount, iptables, Docker, or kernel/rootfs build commands.

Read-only checks performed:

- Repository and docs inspection.
- Source inspection with line numbers.
- `gofmt -l cmd internal web`.
