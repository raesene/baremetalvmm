---
id: ba-acca
status: open
deps: [ba-dec6, ba-1761]
links: []
created: 2026-01-29T18:12:37Z
type: task
priority: 0
assignee: vot3k
parent: ba-0380
---
# Implement orchestrator core (host registry, VM resolution, aggregation)

## Objective
Create internal/orchestrator/orchestrator.go — the core orchestration logic that ties together host management, VM resolution, and cluster-wide operations.

## Location
- New file: internal/orchestrator/orchestrator.go

## Implementation Details

### Orchestrator Struct
```go
type Orchestrator struct {
    hosts    []*HostClient
    strategy PlacementStrategy
    config   *Config
}

type Config struct {
    Hosts     []HostEntry       `json:"hosts"`
    TLS       TLSConfig         `json:"tls"`
    Placement PlacementConfig   `json:"placement"`
}

type HostEntry struct {
    Name    string            `json:"name"`
    Address string            `json:"address"`
    Labels  map[string]string `json:"labels,omitempty"`
}

type PlacementConfig struct {
    Strategy string `json:"strategy"` // "least-loaded" or "round-robin"
}
```

### Constructor
```go
func New(cfg *Config) (*Orchestrator, error)
```
- Create HostClient for each configured host
- Initialize placement strategy from config

### Host Registry Operations
```go
func (o *Orchestrator) AddHost(name, address string, labels map[string]string) error
func (o *Orchestrator) RemoveHost(name string) error
func (o *Orchestrator) ListHosts(ctx context.Context) ([]HostSummary, error)  // Parallel health check
```

**ListHosts** queries all hosts in parallel:
```go
type HostSummary struct {
    Name     string
    Address  string
    Status   *HostStatus  // nil if unreachable
    Healthy  bool
    Error    string       // non-empty if unhealthy
}
```

### VM Resolution
```go
func (o *Orchestrator) ResolveVM(ctx context.Context, vmName string) (*HostClient, *vm.VM, error)
```
- Query all hosts in parallel for the named VM
- Return the host + VM that matches
- Error if VM not found on any host
- Error if VM found on multiple hosts (shouldn't happen with cluster-unique enforcement)

### Cluster-Wide Operations
```go
func (o *Orchestrator) CreateVM(ctx context.Context, req CreateVMRequest, targetHost string) (*vm.VM, string, error)
```
1. If targetHost != "": route to that host directly
2. Else: fetch status from all hosts in parallel
3. Check cluster-unique name (query all hosts for existing VM)
4. Call strategy.SelectHost() for placement
5. Create VM on selected host
6. Return VM + host name

```go
func (o *Orchestrator) ListAllVMs(ctx context.Context) ([]VMWithHost, error)
```
1. Query all hosts in parallel
2. Merge results into unified list with host name attached
```go
type VMWithHost struct {
    *vm.VM
    Host string `json:"host"`
}
```

```go
func (o *Orchestrator) StopVM(ctx context.Context, name string) error
func (o *Orchestrator) StartVM(ctx context.Context, name string) (*vm.VM, error)
func (o *Orchestrator) DeleteVM(ctx context.Context, name string, force bool) error
```
- All resolve the VM to its host first, then proxy the request

```go
func (o *Orchestrator) ClusterStatus(ctx context.Context) (*ClusterSummary, error)
```
```go
type ClusterSummary struct {
    TotalHosts    int
    HealthyHosts  int
    TotalVMs      int
    RunningVMs    int
    TotalCPUs     int
    UsedCPUs      int
    TotalMemoryMB int
    UsedMemoryMB  int
    Hosts         []HostSummary
}
```

### Parallel Query Pattern
All multi-host operations use the same pattern:
```go
func (o *Orchestrator) queryAllHosts(ctx context.Context, fn func(*HostClient) (interface{}, error)) []hostResult
```
- Launch goroutines for each host
- Use errgroup or sync.WaitGroup
- Collect results with timeout (5s per host)
- Skip unreachable hosts (log warning, continue)

### Config File Management
```go
func LoadConfig(path string) (*Config, error)
func (c *Config) Save(path string) error
func DefaultConfigPath() string  // ~/.config/vmm-ctl/config.json
```

## Implementation Notes
- Config file at ~/.config/vmm-ctl/config.json (separate from vmm config)
- AddHost/RemoveHost modify the config file and update in-memory state
- All multi-host queries have a 5-second timeout per host
- Unreachable hosts are logged but don't fail the operation

## Acceptance Criteria
- Host registry supports add/remove/list
- VM resolution finds VMs across hosts
- Cluster-unique name enforcement prevents duplicates
- Placement delegates to configured strategy
- ListAllVMs merges results from all hosts
- ClusterStatus aggregates resources from all hosts
- Unreachable hosts are skipped gracefully

## Acceptance Criteria

Host registry works; VM resolution finds VMs; names are cluster-unique; parallel queries work

