---
id: ba-7b5c
status: open
deps: [ba-acca, ba-dec6, ba-1761]
links: []
created: 2026-01-29T18:13:15Z
type: task
priority: 1
assignee: vot3k
parent: ba-0380
---
# Write unit and integration tests for orchestrator package

## Objective
Write tests for the orchestrator package — client, placement, and core orchestration logic.

## Location
- New file: internal/orchestrator/client_test.go
- New file: internal/orchestrator/placement_test.go
- New file: internal/orchestrator/orchestrator_test.go

## Implementation Details

### Placement Tests (placement_test.go)
- TestLeastLoaded_SelectsMostAvailable: 3 hosts with varying resources → picks most available
- TestLeastLoaded_InsufficientResources: no host has enough → returns error
- TestLeastLoaded_SkipsUnderProvisioned: 2 of 3 hosts have enough → picks best of 2
- TestRoundRobin_DistributesEvenly: 3 hosts, 6 placements → 2 per host
- TestRoundRobin_SkipsInsufficientHosts: skips hosts that can't fit the VM
- TestNewStrategy_ValidNames: "least-loaded" and "round-robin" return correct types
- TestNewStrategy_UnknownName: returns error for unknown strategy

### Client Tests (client_test.go)
Use httptest.NewTLSServer to simulate vmm-api responses.
- TestCreateVM_Success: mock 201 response → returns VM
- TestCreateVM_Conflict: mock 409 response → returns conflict error
- TestListVMs_Success: mock 200 response → returns array
- TestGetStatus_Success: mock status response
- TestHealth_Success: mock health response
- TestClient_NetworkError: unreachable host → returns network error
- TestClient_Timeout: slow server → returns timeout error

### Orchestrator Tests (orchestrator_test.go)
Use multiple httptest servers to simulate a cluster.
- TestListHosts_AllHealthy: all hosts respond → all shown as healthy
- TestListHosts_OneDown: one host down → others still listed, down host shown as unhealthy
- TestResolveVM_Found: VM exists on one host → returns correct host
- TestResolveVM_NotFound: VM on no host → returns error
- TestCreateVM_AutoPlace: no --host → placement strategy invoked
- TestCreateVM_ExplicitHost: --host specified → routes to that host
- TestCreateVM_DuplicateName: name exists on another host → returns error
- TestListAllVMs_MergesHosts: VMs from 3 hosts → merged list with host names
- TestClusterStatus_Aggregates: sums across all hosts

### Test Helpers
```go
func startMockVMMAPI(t *testing.T, vms []*vm.VM, status *HostStatus) *httptest.Server
```
- Returns a mock HTTP server that responds to vmm-api endpoints
- Configurable VM list and host status
- Uses the same JSON envelope format

## Acceptance Criteria
- All placement strategies have test coverage
- Client correctly handles success, error, network failure, and timeout cases
- Orchestrator tests verify multi-host behavior with mock servers
- go test ./internal/orchestrator/ passes
- Tests run without root or network access

## Acceptance Criteria

go test ./internal/orchestrator/ passes; placement, client, and orchestrator logic covered

