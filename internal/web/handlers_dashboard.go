package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/raesene/baremetalvmm/internal/cluster"
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
		"Events":   s.sseBroker.Recent(),
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

	// Flush a comment straight away so the client sees the stream open
	// rather than waiting for the first state change.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: activity\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
