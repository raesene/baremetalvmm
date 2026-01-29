---
id: ba-dca5
status: open
deps: [ba-d968, ba-2a1e]
links: []
created: 2026-01-29T18:10:00Z
type: task
priority: 0
assignee: vot3k
parent: ba-3d4c
---
# Implement HTTP server with mTLS and route registration

## Objective
Create internal/api/server.go — the core HTTP server with mTLS support, route registration, and concurrency-safe access to shared state.

## Location
- New file: internal/api/server.go

## Implementation Details

### Server Struct
```go
type Server struct {
    mu       sync.RWMutex              // RWMutex for state safety
    config   *config.Config
    paths    *config.Paths
    fcClient *firecracker.Client
    netMgr   *network.Manager
    imgMgr   *image.Manager
    mountMgr *mount.Manager
    mux      *http.ServeMux
    logger   *slog.Logger
}
```

### Constructor
```go
func NewServer(cfg *config.Config) *Server
```
- Initialize all internal managers from config (same pattern as CLI main.go)
- Register all routes on the ServeMux
- Apply middleware chain

### Route Registration
Using Go 1.22+ ServeMux method-based routing:
```go
mux.HandleFunc("POST /v1/vms", s.handleCreateVM)
mux.HandleFunc("GET /v1/vms", s.handleListVMs)
mux.HandleFunc("GET /v1/vms/{name}", s.handleGetVM)
mux.HandleFunc("DELETE /v1/vms/{name}", s.handleDeleteVM)
mux.HandleFunc("POST /v1/vms/{name}/start", s.handleStartVM)
mux.HandleFunc("POST /v1/vms/{name}/stop", s.handleStopVM)
mux.HandleFunc("POST /v1/vms/{name}/port-forward", s.handlePortForward)
mux.HandleFunc("GET /v1/images", s.handleListImages)
mux.HandleFunc("POST /v1/images/pull", s.handlePullImages)
mux.HandleFunc("POST /v1/images/import", s.handleImportImage)
mux.HandleFunc("DELETE /v1/images/{name}", s.handleDeleteImage)
mux.HandleFunc("GET /v1/kernels", s.handleListKernels)
mux.HandleFunc("POST /v1/kernels/import", s.handleImportKernel)
mux.HandleFunc("DELETE /v1/kernels/{name}", s.handleDeleteKernel)
mux.HandleFunc("GET /v1/host/status", s.handleHostStatus)
mux.HandleFunc("GET /v1/host/config", s.handleHostConfig)
mux.HandleFunc("GET /v1/health", s.handleHealth)
```

### mTLS Configuration
```go
func (s *Server) ListenAndServeTLS(addr, certFile, keyFile, caFile string) error
```
- Load server cert/key pair
- Load CA certificate pool for client verification
- tls.Config with ClientAuth: tls.RequireAndVerifyClientCert
- MinVersion: tls.VersionTLS13
- Create http.Server with ReadTimeout (30s), WriteTimeout (60s), IdleTimeout (120s)

### Concurrency Safety
- s.mu.RLock() for read operations (list, get, status)
- s.mu.Lock() for write operations (create, start, stop, delete)
- defer unlock in all cases

### Graceful Shutdown
- Accept context.Context
- Listen for SIGINT/SIGTERM
- Call server.Shutdown(ctx) with 30s timeout
- Allow in-flight requests to complete

## Acceptance Criteria
- Server starts and serves on specified address
- mTLS rejects connections without valid client cert
- Routes match RFC-001 API design exactly
- RWMutex provides concurrency safety
- Graceful shutdown on SIGINT/SIGTERM

## Acceptance Criteria

mTLS rejects unauthenticated requests; all RFC routes registered; graceful shutdown works

