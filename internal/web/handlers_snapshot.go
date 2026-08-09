package web

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/snapshot"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
)

// handleSnapshotCreate takes a full snapshot of a running VM. The VM is paused
// briefly and then resumed, so it keeps running afterwards.
func (s *Server) handleSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	snapName := r.FormValue("snapshot_name")
	if err := validate.SnapshotName(snapName); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(v)
	if v.State != vm.StateRunning {
		httpError(w, r, "VM must be running to take a snapshot", http.StatusConflict)
		return
	}

	snapMgr := snapshot.NewManager(paths.Snapshots)
	if snapMgr.Exists(name, snapName) {
		httpError(w, r, fmt.Sprintf("snapshot %q already exists", snapName), http.StatusConflict)
		return
	}

	if _, err := snapMgr.Create(context.Background(), fcClient, v, snapName, true); err != nil {
		httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/vms/"+name, http.StatusSeeOther)
}

// handleSnapshotRestore rolls a VM back to one of its snapshots in place. If the
// VM is running it is stopped first, then restored and resumed from the snapshot.
func (s *Server) handleSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	snapName := chi.URLParam(r, "snapshot")
	if err := validate.SnapshotName(snapName); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	snapMgr := snapshot.NewManager(paths.Snapshots)
	meta, err := snapMgr.Get(name, snapName)
	if err != nil {
		httpError(w, r, err.Error(), http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(v)
	ctx := context.Background()
	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)

	// The VM must be stopped before its disks are overwritten.
	if v.State == vm.StateRunning {
		v.State = vm.StateStopping
		v.Save(paths.VMs)
		if err := fcClient.Terminate(ctx, v); err != nil {
			v.State = vm.StateError
			v.Save(paths.VMs)
			httpError(w, r, err.Error(), http.StatusInternalServerError)
			return
		}
		if v.TapDevice != "" && netMgr.TapExists(v.TapDevice) {
			netMgr.DeleteTap(v.TapDevice)
		}
		v.State = vm.StateStopped
		v.Save(paths.VMs)
	}

	// Restore uses the snapshot's resource configuration (memory size is fixed).
	v.CPUs = meta.CPUs
	v.MemoryMB = meta.MemoryMB

	logPath := fmt.Sprintf("%s/%s.log", paths.Logs, name)
	if _, err := snapMgr.Restore(ctx, fcClient, netMgr, v, snapName, logPath, true); err != nil {
		v.State = vm.StateError
		v.Save(paths.VMs)
		httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}
	v.Save(paths.VMs)

	http.Redirect(w, r, "/vms/"+name, http.StatusSeeOther)
}

// handleSnapshotDelete removes a single snapshot.
func (s *Server) handleSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	snapName := chi.URLParam(r, "snapshot")
	if err := validate.SnapshotName(snapName); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	snapMgr := snapshot.NewManager(paths.Snapshots)
	if err := snapMgr.Delete(name, snapName); err != nil {
		httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/vms/"+name, http.StatusSeeOther)
}

// JSON API handlers

func (s *Server) handleAPISnapshotList(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()
	snapMgr := snapshot.NewManager(paths.Snapshots)
	snaps, err := snapMgr.List(name)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, snaps)
}

func (s *Server) handleAPISnapshotCreate(w http.ResponseWriter, r *http.Request) {
	s.handleSnapshotCreate(w, r)
}

func (s *Server) handleAPISnapshotRestore(w http.ResponseWriter, r *http.Request) {
	s.handleSnapshotRestore(w, r)
}

func (s *Server) handleAPISnapshotDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	snapName := chi.URLParam(r, "snapshot")
	if err := validate.SnapshotName(snapName); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()
	snapMgr := snapshot.NewManager(paths.Snapshots)
	if err := snapMgr.Delete(name, snapName); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"})
}
