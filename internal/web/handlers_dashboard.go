package web

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/vm"
)

type DashboardStats struct {
	TotalVMs      int
	RunningVMs    int
	StoppedVMs    int
	TotalClusters int
	TotalCPUs     int
	TotalMemoryMB int
	TotalDiskMB   int
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()

	vms, _ := vm.List(paths.VMs)
	fcClient := firecracker.NewClient()
	for _, v := range vms {
		fcClient.UpdateVMState(v)
	}

	clusters, _ := cluster.List(paths.Clusters)

	stats := DashboardStats{
		TotalVMs:      len(vms),
		TotalClusters: len(clusters),
	}
	for _, v := range vms {
		switch v.State {
		case vm.StateRunning:
			stats.RunningVMs++
		case vm.StateStopped, vm.StateCreated:
			stats.StoppedVMs++
		}
		stats.TotalCPUs += v.CPUs
		stats.TotalMemoryMB += v.MemoryMB
		stats.TotalDiskMB += v.DiskSizeMB
	}

	s.renderPage(w, r, "dashboard.html", "dashboard", map[string]interface{}{
		"Stats":    stats,
		"VMs":      vms,
		"Clusters": clusters,
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.sseBroker.Subscribe()
	defer s.sseBroker.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-ch:
			fmt.Fprintf(w, "event: vm-update\ndata: %s\n\n", event)
			flusher.Flush()
		}
	}
}

type SSEBroker struct {
	subscribers map[chan string]struct{}
	subscribe   chan chan string
	unsubscribe chan chan string
	broadcast   chan string
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		subscribers: make(map[chan string]struct{}),
		subscribe:   make(chan chan string),
		unsubscribe: make(chan chan string),
		broadcast:   make(chan string, 16),
	}
}

func (b *SSEBroker) Subscribe() chan string {
	ch := make(chan string, 16)
	b.subscribe <- ch
	return ch
}

func (b *SSEBroker) Unsubscribe(ch chan string) {
	b.unsubscribe <- ch
}

func (b *SSEBroker) Start(cfg *config.Config) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastStates map[string]string

	for {
		select {
		case ch := <-b.subscribe:
			b.subscribers[ch] = struct{}{}
		case ch := <-b.unsubscribe:
			delete(b.subscribers, ch)
			close(ch)
		case msg := <-b.broadcast:
			for ch := range b.subscribers {
				select {
				case ch <- msg:
				default:
				}
			}
		case <-ticker.C:
			currentStates := pollVMStates(cfg)
			if lastStates != nil {
				for name, state := range currentStates {
					if lastStates[name] != state {
						msg := fmt.Sprintf(`{"name":"%s","state":"%s"}`, name, state)
						for ch := range b.subscribers {
							select {
							case ch <- msg:
							default:
							}
						}
					}
				}
				for name := range lastStates {
					if _, ok := currentStates[name]; !ok {
						msg := fmt.Sprintf(`{"name":"%s","state":"deleted"}`, name)
						for ch := range b.subscribers {
							select {
							case ch <- msg:
							default:
							}
						}
					}
				}
			}
			lastStates = currentStates
		}
	}
}

func pollVMStates(cfg *config.Config) map[string]string {
	paths := cfg.GetPaths()
	vms, err := vm.List(paths.VMs)
	if err != nil {
		log.Printf("SSE poll error: %v", err)
		return nil
	}

	fcClient := firecracker.NewClient()
	states := make(map[string]string)
	for _, v := range vms {
		fcClient.UpdateVMState(v)
		states[v.Name] = string(v.State)
	}
	return states
}
