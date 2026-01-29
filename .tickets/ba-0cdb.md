---
id: ba-0cdb
status: open
deps: []
links: []
created: 2026-01-29T18:09:20Z
type: epic
priority: 2
assignee: vot3k
---
# Epic: Phase 3 — Hardening & Operational Readiness

## Overview
Production-grade reliability, TLS certificate management tooling, retry logic, image distribution, and build/install infrastructure updates. This is Phase 3 of RFC-001.

## Scope
- TLS certificate generation tooling (vmm-ctl tls init)
- Retry and timeout logic (exponential backoff)
- Image synchronization across hosts (stretch goal)
- Makefile and GoReleaser updates for vmm-api and vmm-ctl binaries
- Install script updates
- OpenAPI spec or API documentation

## Success Criteria
- vmm-ctl tls init generates CA + server/client certs
- Client retries transient failures with exponential backoff
- make build produces all three binaries (vmm, vmm-api, vmm-ctl)
- GoReleaser packages vmm-api and vmm-ctl in releases
- Install script optionally deploys vmm-api

## Dependencies
- Requires Phase 1 and Phase 2 to be functional

## Reference
- RFC: /Users/jimmy/Tools/baremetalvmm/docs/rfcs/RFC-001-multi-host-orchestration-api.md

