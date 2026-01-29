---
id: ba-3d4c
status: open
deps: []
links: []
created: 2026-01-29T18:09:19Z
type: epic
priority: 1
assignee: vot3k
---
# Epic: Phase 1 — Host-Level REST API (vmm-api)

## Overview
Implement the vmm-api daemon — a REST API server that runs on each host, wrapping existing internal/* packages over HTTPS. This is Phase 1 of RFC-001 (Multi-Host Orchestration API).

## Scope
- New binary: cmd/vmm-api/main.go
- New package: internal/api/ (server, handlers, responses, middleware)
- Concurrency safety via sync.RWMutex on state-mutating operations
- mTLS authentication support
- Systemd service integration
- Host status endpoint (CPU/memory reporting)

## Success Criteria
- curl -X POST https://host:8443/v1/vms -d '{"name":"test"}' creates a VM
- curl https://host:8443/v1/vms returns JSON list of all VMs
- curl https://host:8443/v1/host/status returns CPU/memory info
- All endpoints use consistent JSON envelope: {"data": ..., "error": ...}
- mTLS enforced — no unauthenticated access

## Reference
- RFC: /Users/jimmy/Tools/baremetalvmm/docs/rfcs/RFC-001-multi-host-orchestration-api.md
- Module: github.com/raesene/baremetalvmm
- Go version: 1.25.3
- Zero new external dependencies (stdlib net/http, crypto/tls, encoding/json)

