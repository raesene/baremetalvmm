---
id: ba-2a1e
status: open
deps: []
links: []
created: 2026-01-29T18:10:00Z
type: task
priority: 1
assignee: vot3k
parent: ba-3d4c
---
# Implement API middleware (logging, panic recovery, request ID)

## Objective
Create internal/api/middleware.go with HTTP middleware for request logging, panic recovery, and request ID generation.

## Location
- New file: internal/api/middleware.go

## Implementation Details

### Request Logging Middleware
- Log each request: method, path, status code, duration
- Use Go's stdlib log/slog (structured logging)
- Log at INFO level for successful requests, WARN for 4xx, ERROR for 5xx
- Include request ID in log context

### Panic Recovery Middleware
- Wrap handlers in deferred recover()
- On panic: log stack trace at ERROR level, return 500 with internal_error response
- Prevents the entire server from crashing on handler panic

### Request ID Middleware
- Generate UUID-based request ID for each request
- Add to response header: X-Request-ID
- Add to request context for downstream use

### Middleware Chain
- Provide a `Chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler` helper
- Apply order: RequestID → Logging → PanicRecovery → Handler

## No External Dependencies
- Use crypto/rand for UUID generation (no uuid package needed for just request IDs)
- Use log/slog for structured logging
- Use net/http standard middleware pattern: func(http.Handler) http.Handler

## Acceptance Criteria
- Requests are logged with method, path, status, duration
- Panics in handlers are caught and return 500 JSON error responses
- X-Request-ID header is present on all responses
- No external dependencies added

## Acceptance Criteria

Panic in handler returns 500 JSON, X-Request-ID on all responses

