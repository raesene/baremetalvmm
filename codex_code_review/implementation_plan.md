# Implementation Plan — Code Review Findings

Based on the Codex code review (2026-05-25). Each finding is assessed for validity, risk, effort, and includes a safe implementation approach.

## Assessment Summary

| # | Finding | Valid? | Risk | Effort | Priority |
|---|---------|--------|------|--------|----------|
| 1 | Name validation / path traversal | Yes | High | Low | **P1** |
| 2 | File permissions too open | Yes | Medium | Low | **P1** |
| 3 | Download integrity (no checksums) | Yes | Medium | Medium | **P3** |
| 4 | Non-atomic state writes, no locking | Yes | Medium | Medium | **P2** |
| 5 | No Firecracker jailer | Yes, known | Low | High | **P4** |
| 6 | WebSocket origin check disabled | Yes | High | Low | **P1** |
| 7 | Login rate limiting / password handling | Yes | Medium | Low | **P2** |
| 8 | Lifecycle cleanup / resource leaks | Yes | Medium | Medium | **P3** |
| 9 | PID-based kill risks | Partly | Low | Low | **P3** |
| 10 | CLI/web lifecycle divergence | Yes | Medium | High | **P4** |
| 11 | Hardcoded /16 subnet | Yes | Low | Medium | **P3** |
| 12 | Port-forward not idempotent | Yes | Low | Low | **P3** |
| 13 | Web image downloads trust client URLs | Yes | Medium | Low | **P2** |
| 14 | Shell injection in cluster provisioning | Yes | Medium | Low | **P2** |
| 15 | HTTP server lacks timeouts | Yes | Medium | Low | **P1** |
| 16 | CSRF logic issues | Yes | Medium | Low | **P2** |
| 17 | Resource/DNS validation | Yes | Low | Low | **P2** |
| 18 | Install/uninstall script issues | Yes | Low | Low | **P3** |
| 19 | gofmt not clean | Yes | Cosmetic | Trivial | **P1** |
| 20 | No Go tests | Yes | Process | Ongoing | **P2** |
| 21 | Documentation inconsistencies | Yes | Low | Trivial | **P3** |
| 22 | CSP / inline onclick | Yes | Low | Trivial | **P1** |

---

## P1 — Quick wins and high-impact fixes

These are low effort, high value, and unlikely to break existing functionality.

### 1.1 Run gofmt (Finding 19)

**What**: Run `gofmt -w` on all Go source files.
**Risk of breakage**: None — formatting only.
**Verification**: `gofmt -l cmd internal web` returns no output.
**Effort**: 5 minutes.

### 1.2 Fix inline onclick violating CSP (Finding 22)

**What**: Replace `onclick="copyKey()"` in `web/templates/api_key.html` with a `data-action` attribute and add an event listener in `web/static/app.js`.
**Risk of breakage**: Very low — only affects the API key copy button.
**Verification**: Test the API key page in a browser, confirm the copy button works and no CSP errors appear in the console.
**Effort**: 15 minutes.

### 1.3 Add identifier validation (Finding 1)

**What**: Create `internal/validate/validate.go` with an `Identifier(kind, value string) error` function. Pattern: `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. Reject empty, `.`, `..`, path separators, control characters.
**Where to apply**: Add calls at the entry points — CLI command functions in `cmd/vmm/main.go` and web handlers in `internal/web/handlers_vm.go`, `handlers_cluster.go`, `handlers_images.go`. This is safer than changing the lower-level `vm.Save`/`cluster.Save` functions because it preserves existing behavior for already-stored VMs.
**Risk of breakage**: Low — only rejects names that would have caused problems anyway. Existing VMs with valid names are unaffected.
**Verification**: Create a VM with a name like `../../etc/test` — should get a clear error. Existing VMs should list/start/stop normally.
**Effort**: 1-2 hours.

### 1.4 Tighten file permissions (Finding 2)

**What**: Change `os.MkdirAll` calls in `internal/config/config.go` from `0755` to `0700` for sensitive directories (vms, clusters, sockets, ssh, state). Change `os.WriteFile` in `internal/vm/vm.go` and `internal/cluster/cluster.go` from `0644` to `0600`.
**Risk of breakage**: Low — vmm runs as root, so root can always read its own files. The only risk is if another process reads these files as a non-root user, which would be unusual.
**Verification**: Run `vmm create test1`, check that `/var/lib/vmm/vms/test1.json` is `0600`. Run `stat` on the directories.
**Effort**: 30 minutes.

### 1.5 Fix WebSocket origin check (Finding 6)

**What**: Remove `InsecureSkipVerify: true` from the WebSocket accept options in `internal/web/handlers_terminal.go`. Add an explicit origin check that allows the configured listen address.
**Risk of breakage**: Medium — if users access the web UI through a reverse proxy or non-standard hostname, the origin check could reject legitimate connections. Mitigation: add a `--allowed-origins` flag with a sensible default (the listen address), and log rejected origins clearly.
**Verification**: Open the terminal in a browser at the configured address — should work. Open from a different origin — should be rejected.
**Effort**: 1 hour.

### 1.6 Add HTTP server timeouts (Finding 15)

**What**: Replace `http.ListenAndServe` in `internal/web/server.go` with an `http.Server` struct with `ReadHeaderTimeout: 10s`, `ReadTimeout: 30s`, `WriteTimeout: 60s`, `IdleTimeout: 120s`. Add graceful shutdown on SIGINT/SIGTERM.
**Risk of breakage**: Low — timeouts are generous. The only concern is SSE and WebSocket connections, which need special handling. SSE is already using `http.Flusher` — ensure the write timeout doesn't kill long-lived SSE streams (may need per-handler timeout override or use `http.ResponseController`). WebSocket terminal connections also need to not be killed by write timeouts.
**Verification**: Start vmm-web, verify pages load. Open terminal, verify it stays connected. Open SSE event stream, verify it stays connected.
**Effort**: 1-2 hours.

---

## P2 — Important fixes requiring some care

### 2.1 Fix CSRF logic (Finding 16)

**What**: 
- Generate a separate CSRF token per session (not reuse the session token).
- Only skip CSRF when the Bearer token is actually valid, not just present.
- Store the CSRF token alongside the session.
**Risk of breakage**: Medium — any existing API integrations using Bearer auth should still work. Browser sessions will need to use the new CSRF token. Since the CSRF token is already rendered into forms via `CSRFToken`, the template side should work automatically once we change what `CSRFToken` returns.
**Verification**: Test login, VM create/start/stop/delete from the web UI. Test API calls with Bearer token. Test that a forged CSRF token is rejected.
**Effort**: 1-2 hours.

### 2.2 Fix rate limiter keying (Finding 7)

**What**: Use `net.SplitHostPort` to strip the port from `r.RemoteAddr` before using it as a rate limiter key.
**Risk of breakage**: Very low.
**Verification**: Multiple failed logins from the same IP should trigger the rate limit.
**Effort**: 15 minutes.

### 2.3 Reject default/weak passwords (Finding 7)

**What**: At startup, refuse to start if `VMM_WEB_PASSWORD` is `changeme`, empty, or shorter than 8 characters. Log a clear error message explaining how to set a proper password.
**Risk of breakage**: Medium — breaks existing deployments using `changeme`. This is intentional but needs clear messaging and documentation.
**Verification**: Start vmm-web with `VMM_WEB_PASSWORD=changeme` — should refuse. Start with a proper password — should work.
**Effort**: 15 minutes.

### 2.4 Handle CSPRNG failures (Finding 7)

**What**: Check the error return from `rand.Read` in `internal/web/auth.go` and `server.go`. If it fails, refuse to generate tokens (fail closed).
**Risk of breakage**: None in practice — `crypto/rand.Read` essentially never fails on Linux.
**Effort**: 10 minutes.

### 2.5 Server-side URL resolution for image downloads (Finding 13)

**What**: Change the web image/kernel download handlers to accept a release tag/type instead of a raw URL. Resolve the URL server-side from the release list.
**Risk of breakage**: Medium — changes the web UI form behavior. The forms that populate download options already come from release listings, so the change is to submit the release identifier rather than the URL.
**Verification**: Download a kernel and rootfs from the web UI images page. Confirm the correct files are downloaded.
**Effort**: 2-3 hours.

### 2.6 Validate cluster provisioning inputs (Finding 14)

**What**: Apply identifier validation to cluster/node names. Add strict semver validation for Kubernetes versions (e.g., `^1\.\d{1,2}\.\d{1,3}$`). Use shell quoting (`%q` or explicit escaping) for all interpolated values in SSH commands.
**Risk of breakage**: Low — only rejects malformed input that would have failed anyway.
**Verification**: Create a cluster with a normal name/version — should work. Try with a malicious name — should be rejected at validation.
**Effort**: 1-2 hours.

### 2.7 Resource and DNS validation (Finding 17)

**What**: Add bounds checking for `--cpus` (1-32), `--memory` (128-65536 MB), `--disk` (256-1048576 MB). Validate DNS servers with `net.ParseIP`. Apply in both CLI and web handlers.
**Risk of breakage**: Low — only rejects clearly invalid values.
**Verification**: `vmm create test --cpus 0` should fail. `vmm create test --cpus 4` should work. `vmm create test --dns "8.8.8.8; malicious"` should fail.
**Effort**: 1 hour.

### 2.8 Add initial Go tests (Finding 20)

**What**: Start with tests for the new validation package (finding 1.3), then add tests for:
- IP allocation in `internal/network`
- iptables rule rendering
- Auth/CSRF logic in `internal/web`
- VM/cluster persistence (atomic writes if implemented)
**Risk of breakage**: None — tests don't change production code.
**Verification**: `go test ./...` passes.
**Effort**: Ongoing, 1-2 hours per package.

### 2.9 Add atomic state writes with file locking (Finding 4)

**What**: Change `vm.Save` and `cluster.Save` to write to a temp file, fsync, then rename. Add flock-based locking around state-changing operations.
**Risk of breakage**: Medium — the rename approach is safe but any locking bugs could cause deadlocks. Start with advisory locking (flock) and keep lock scopes small.
**Verification**: Save a VM, verify the JSON is valid. Kill vmm mid-save (hard to test manually but the atomic write ensures either old or new state survives). Run two concurrent `vmm start` commands — should not produce duplicate IPs.
**Effort**: 2-3 hours.

---

## P3 — Good improvements, lower urgency

### 3.1 Download integrity verification (Finding 3)

**What**: Publish SHA256 checksums alongside release assets. Verify checksums after download in `internal/image`. Use `mktemp -d` in install scripts instead of fixed `/tmp` paths.
**Risk of breakage**: Low for verification (fails on mismatch, which is correct). Publishing checksums requires updating the release workflow.
**Verification**: Download an image, verify checksum matches. Tamper with a checksum — download should fail.
**Effort**: 3-4 hours (includes CI changes).

### 3.2 Lifecycle cleanup/rollback (Finding 8)

**What**: Build a cleanup stack in VM start — track each resource created (TAP, iptables rules, socket, mount images). On failure, unwind in reverse order.
**Risk of breakage**: Medium — cleanup code can itself fail. Be conservative: log cleanup failures but don't panic.
**Verification**: Force a start failure (e.g., bad kernel path). Verify no orphaned TAP devices or iptables rules remain.
**Effort**: 3-4 hours.

### 3.3 PID verification before kill (Finding 9)

**What**: Before sending SIGKILL, check `/proc/<pid>/cmdline` contains "firecracker" and the expected socket path. The review overstates this slightly — there is already a `Signal(0)` liveness check — but there's no process identity verification.
**Risk of breakage**: Low — worst case, a stale PID is not killed and requires manual cleanup, which is safer than killing the wrong process.
**Verification**: Start a VM, note PID. Stop it. Verify the PID is no longer targeted if reused by another process.
**Effort**: 1 hour.

### 3.4 Port-forward idempotency (Finding 12)

**What**: Use `iptables -C` to check for existing rules before appending. Validate port ranges (1-65535). Default bind address to `127.0.0.1`.
**Risk of breakage**: The `127.0.0.1` default is a behavior change — existing users expecting `0.0.0.0` will need to specify it explicitly. Consider making this opt-in initially or documenting the change.
**Verification**: Add the same port forward twice — only one iptables rule should exist.
**Effort**: 1-2 hours.

### 3.5 Fix hardcoded /16 subnet (Finding 11)

**What**: Parse `cfg.Subnet` with `net/netip`, derive prefix length and netmask from it. Use the configured CIDR in bridge setup, IP allocation, and kernel args.
**Risk of breakage**: Medium — if the parsing/derivation has a bug, networking breaks for all VMs. Needs careful testing.
**Verification**: Test with default subnet. Test with a custom `/24` subnet. Verify bridge config, guest networking, and NAT rules all use the correct prefix.
**Effort**: 2-3 hours.

### 3.6 Install/uninstall script fixes (Finding 18)

**What**: Use `mktemp -d` in install.sh. Add vmm-web.service cleanup to uninstall.sh. Fix NAT rule cleanup to match what the Go code creates.
**Risk of breakage**: Low — install is a fresh operation, uninstall improvements only add missing cleanup.
**Verification**: Run install on a clean system. Run uninstall — verify no leftover services or rules.
**Effort**: 1 hour.

### 3.7 Documentation fixes (Finding 21)

**What**: Align Go version references (README, CLAUDE.md, go.mod, CI). Fix vmm-web listen address in docs. Document API cluster creation SSH key behavior.
**Risk of breakage**: None.
**Effort**: 30 minutes.

---

## P4 — Significant effort, defer for now

### 4.1 Shared service layer (Finding 10)

**What**: Extract VM lifecycle operations into `internal/service` so CLI, web, and cluster code share one implementation.
**Why defer**: This is the right architecture long-term, but it's a large refactor with high risk of introducing regressions. The current duplication is manageable.
**Approach when ready**: Start with `StartVM` (the most divergent path — CLI handles mounts, web doesn't). Extract step by step, verifying each operation still works.
**Effort**: 8-12 hours.

### 4.2 Firecracker jailer integration (Finding 5)

**What**: Run Firecracker under the jailer with a dedicated unprivileged UID, chroot, and cgroup limits.
**Why defer**: This is a significant feature addition, not a bug fix. PLAN.md already tracks it as a known gap. The current setup is reasonable for a development tool on a single-user host.
**Effort**: 12-20 hours.

---

## Implementation Order

The recommended order minimizes risk while addressing the highest-value items first:

1. **gofmt** (1.1) — pure formatting, zero risk
2. **Fix CSP onclick** (1.2) — trivial, fixes a real bug
3. **Add identifier validation** (1.3) — the single highest-value security fix
4. **Tighten file permissions** (1.4) — small change, meaningful security improvement
5. **HTTP server timeouts** (1.6) — low risk, important for any network-exposed deployment
6. **Rate limiter fix** (2.2) — trivial fix
7. **CSPRNG error handling** (2.4) — trivial fix
8. **WebSocket origin check** (1.5) — important but needs careful testing with the terminal
9. **CSRF fixes** (2.1) — important, moderate complexity
10. **Reject weak passwords** (2.3) — intentional breaking change, needs clear messaging
11. **Resource/DNS validation** (2.7) — straightforward
12. **Cluster input validation** (2.6) — straightforward
13. **Server-side URL resolution** (2.5) — moderate complexity
14. **Atomic state writes** (2.9) — moderate complexity, needs testing
15. **Initial test suite** (2.8) — ongoing, build alongside other changes
16. **PID verification** (3.3), **Port-forward fixes** (3.4), **Lifecycle cleanup** (3.2) — as time allows
17. **Subnet fix** (3.5), **Script fixes** (3.6), **Docs** (3.7) — as time allows
18. **Service layer** (4.1), **Jailer** (4.2) — future work

## Testing Strategy

For each change, before merging:

1. **Build**: `go build ./cmd/vmm/ && go build ./cmd/vmm-web/` — must succeed
2. **Format**: `gofmt -l cmd internal web` — must return empty
3. **Unit tests**: `go test ./...` — must pass (once tests exist)
4. **Manual smoke test** on test server (192.168.41.108):
   - `vmm create test1 --cpus 1 --memory 512`
   - `vmm start test1`
   - `vmm list` shows test1 running
   - `vmm ssh test1` connects
   - `vmm stop test1` and `vmm delete test1` clean up
5. **Web UI smoke test** (for web-related changes):
   - Login, create VM, start, open terminal, stop, delete
   - Check browser console for CSP errors
6. **Negative tests** for validation changes:
   - Try invalid names, ports, resources — should get clear errors
