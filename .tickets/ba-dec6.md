---
id: ba-dec6
status: open
deps: [ba-dca5]
links: []
created: 2026-01-29T18:12:36Z
type: task
priority: 0
assignee: vot3k
parent: ba-0380
---
# Implement orchestrator HTTP client with mTLS

## Objective
Create internal/orchestrator/client.go — an HTTP client that communicates with vmm-api instances using mTLS.

## Location
- New file: internal/orchestrator/client.go

## Implementation Details

### HostClient Struct
```go
type HostClient struct {
    Name       string
    Address    string           // e.g., "192.168.1.10:8443"
    Labels     map[string]string
    httpClient *http.Client
}
```

### Constructor
```go
func NewHostClient(name, address string, labels map[string]string, tlsCfg *TLSConfig) (*HostClient, error)
```

### TLS Configuration
```go
type TLSConfig struct {
    CACert     string // Path to CA certificate
    ClientCert string // Path to client certificate
    ClientKey  string // Path to client private key
}

func (t *TLSConfig) BuildTLSConfig() (*tls.Config, error)
```
- Load CA cert into x509.CertPool
- Load client cert/key pair
- Return tls.Config with MinVersion TLS 1.3

### Client Methods (one per API endpoint)
Each method makes an HTTP request and decodes the JSON envelope response.

**VM Operations:**
```go
func (c *HostClient) CreateVM(ctx context.Context, req CreateVMRequest) (*vm.VM, error)
func (c *HostClient) ListVMs(ctx context.Context) ([]*vm.VM, error)
func (c *HostClient) GetVM(ctx context.Context, name string) (*vm.VM, error)
func (c *HostClient) DeleteVM(ctx context.Context, name string, force bool) error
func (c *HostClient) StartVM(ctx context.Context, name string) (*vm.VM, error)
func (c *HostClient) StopVM(ctx context.Context, name string) (*vm.VM, error)
```

**Host Operations:**
```go
func (c *HostClient) GetStatus(ctx context.Context) (*HostStatus, error)
func (c *HostClient) Health(ctx context.Context) (*HealthResponse, error)
```

### Request/Response Types
```go
type CreateVMRequest struct {
    Name        string     `json:"name"`
    CPUs        int        `json:"cpus,omitempty"`
    MemoryMB    int        `json:"memory_mb,omitempty"`
    DiskSizeMB  int        `json:"disk_size_mb,omitempty"`
    SSHPublicKey string    `json:"ssh_public_key,omitempty"`
    DNSServers  []string   `json:"dns_servers,omitempty"`
    Image       string     `json:"image,omitempty"`
    Kernel      string     `json:"kernel,omitempty"`
}

type HostStatus struct {
    Hostname         string     `json:"hostname"`
    TotalCPUs        int        `json:"total_cpus"`
    UsedCPUs         int        `json:"used_cpus"`
    TotalMemoryMB    int        `json:"total_memory_mb"`
    UsedMemoryMB     int        `json:"used_memory_mb"`
    AvailableMemoryMB int       `json:"available_memory_mb"`
    RunningVMs       int        `json:"running_vms"`
    TotalVMs         int        `json:"total_vms"`
}
```

### Error Handling
- Parse API error envelope and return typed errors
- Distinguish between network errors (host unreachable) and API errors (VM not found)
- Include host name in error messages for debugging: "host-a: vm not found: worker-1"

### HTTP Client Configuration
- Timeout: 30s default, 300s for long operations (image import)
- No automatic retries in the client layer (orchestrator handles retry policy)
- Connection pooling: MaxIdleConns=10, MaxIdleConnsPerHost=5

## Acceptance Criteria
- Client makes authenticated mTLS requests to vmm-api
- All vmm-api endpoints have corresponding client methods
- JSON envelope is correctly parsed for both success and error cases
- Network errors are distinguishable from API errors
- Timeouts are configurable

## Acceptance Criteria

Client makes mTLS requests; all endpoints wrapped; network vs API errors distinguished

