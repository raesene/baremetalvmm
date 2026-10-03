package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/portforward"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
)

// portForwardRow is a forward with the display details templates need.
type portForwardRow struct {
	portforward.Entry
	HostAddr  string // where other machines reach it, e.g. 192.168.1.10:8080
	ChipClass string // chip-* class for the status
}

func (s *Server) netManager() *network.Manager {
	return network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
}

// portForwardRows lists the forwards on vms, which must have current state.
func (s *Server) portForwardRows(vms []*vm.VM) []portForwardRow {
	hostIP := network.InterfaceIPv4(s.cfg.HostInterface)
	entries := portforward.List(s.netManager(), vms)
	rows := make([]portForwardRow, len(entries))
	for i, e := range entries {
		rows[i] = portForwardRow{Entry: e, HostAddr: fmt.Sprintf("%s:%d", hostIP, e.HostPort)}
		switch e.Status {
		case portforward.StatusActive:
			rows[i].ChipClass = "chip-running"
		case portforward.StatusMissing:
			rows[i].ChipClass = "chip-error"
		case portforward.StatusOutdated:
			rows[i].ChipClass = "chip-created"
		default:
			rows[i].ChipClass = "chip-stopped"
		}
	}
	return rows
}

// loadAllVMs returns every VM with its current state.
func loadAllVMs(vmsDir string) ([]*vm.VM, error) {
	all, err := vm.List(vmsDir)
	if err != nil {
		return nil, err
	}
	fcClient := firecracker.NewClient()
	for _, v := range all {
		fcClient.UpdateVMState(v)
	}
	return all, nil
}

// handlePortForwards renders the host-wide list of forwards.
func (s *Server) handlePortForwards(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{"HostIP": network.InterfaceIPv4(s.cfg.HostInterface)}
	all, err := loadAllVMs(s.cfg.GetPaths().VMs)
	if err != nil {
		data["Flash"], data["FlashType"] = "Failed to list VMs: "+err.Error(), "error"
	} else {
		s.addForwardRows(data, all)
	}
	s.renderPage(w, r, "port_forwards.html", "port-forwards", data)
}

// addForwardRows puts the forwards for vms into a page's data, flagging any
// that need re-applying so the page can explain the fix.
func (s *Server) addForwardRows(data map[string]interface{}, vms []*vm.VM) {
	rows := s.portForwardRows(vms)
	data["PortForwards"] = rows
	for _, row := range rows {
		if row.Status == portforward.StatusMissing || row.Status == portforward.StatusOutdated {
			data["ForwardsMissing"] = true
		}
	}
}

// formForward reads host_port, guest_port and protocol from a form.
func formForward(r *http.Request) (vm.PortForward, error) {
	hostPort, err1 := strconv.Atoi(r.FormValue("host_port"))
	guestPort, err2 := strconv.Atoi(r.FormValue("guest_port"))
	if err1 != nil || err2 != nil {
		return vm.PortForward{}, errors.New("host and guest ports must be numbers")
	}
	pf := vm.PortForward{HostPort: hostPort, GuestPort: guestPort, Protocol: r.FormValue("protocol")}
	if pf.Protocol == "" {
		pf.Protocol = "tcp"
	}
	return pf, portforward.Validate(pf)
}

// addForward adds pf to the named VM, saves it and records the event.
func (s *Server) addForward(r *http.Request, name string, pf vm.PortForward) (live bool, code int, err error) {
	paths := s.cfg.GetPaths()
	all, err := loadAllVMs(paths.VMs)
	if err != nil {
		return false, http.StatusInternalServerError, err
	}
	var v *vm.VM
	for _, candidate := range all {
		if candidate.Name == name {
			v = candidate
		}
	}
	if v == nil {
		return false, http.StatusNotFound, errors.New("VM not found")
	}

	netMgr := s.netManager()
	live, err = portforward.Add(netMgr, all, v, pf)
	if err != nil {
		if errors.Is(err, portforward.ErrExists) {
			return false, http.StatusConflict, fmt.Errorf("port forward %s already exists", portforward.Format(pf))
		}
		return false, http.StatusConflict, err
	}
	if err := v.Save(paths.VMs); err != nil {
		if live {
			_ = netMgr.RemovePortForward(pf.HostPort, pf.GuestPort, v.IPAddress, pf.Protocol)
		}
		return false, http.StatusInternalServerError, fmt.Errorf("failed to save VM: %w", err)
	}

	detail := "added " + portforward.Format(pf)
	if !live {
		detail += " (applies on start)"
	}
	s.sseBroker.Note(kindVM, name, "port-forward", requestSource(r), detail)
	return live, http.StatusCreated, nil
}

// removeForward removes a forward from the named VM. An empty protocol
// matches either if only one forward has those ports.
func (s *Server) removeForward(r *http.Request, name string, hostPort, guestPort int, protocol string) (int, error) {
	paths := s.cfg.GetPaths()
	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		return http.StatusNotFound, errors.New("VM not found")
	}
	i, err := portforward.Find(v, hostPort, guestPort, protocol)
	if errors.Is(err, portforward.ErrNotFound) {
		return http.StatusNotFound, fmt.Errorf("port forward %d:%d not found", hostPort, guestPort)
	}
	if err != nil {
		return http.StatusBadRequest, err
	}
	removed, err := portforward.Remove(s.netManager(), v, i)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to remove firewall rules: %w", err)
	}
	if err := v.Save(paths.VMs); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("firewall rules removed, but failed to save VM: %w", err)
	}
	s.sseBroker.Note(kindVM, name, "port-forward", requestSource(r), "removed "+portforward.Format(removed))
	return http.StatusOK, nil
}

// returnPath is where a form post goes back to: the port forwards page if it
// asked for that, otherwise the VM's page. Only these two are allowed, so the
// field can't be used as an open redirect.
func returnPath(r *http.Request, name string) string {
	if r.FormValue("return") == "port-forwards" {
		return "/port-forwards"
	}
	return "/vms/" + name
}

// renderForwardError shows a failed form action on the page it came from.
func (s *Server) renderForwardError(w http.ResponseWriter, r *http.Request, name string, err error) {
	if returnPath(r, name) == "/port-forwards" {
		data := map[string]interface{}{
			"HostIP": network.InterfaceIPv4(s.cfg.HostInterface),
			"Flash":  err.Error(), "FlashType": "error",
		}
		if all, lerr := loadAllVMs(s.cfg.GetPaths().VMs); lerr == nil {
			s.addForwardRows(data, all)
		}
		s.renderPage(w, r, "port_forwards.html", "port-forwards", data)
		return
	}
	v, lerr := vm.Load(s.cfg.GetPaths().VMs, name)
	if lerr != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	firecracker.NewClient().UpdateVMState(v)
	s.renderVMDetail(w, r, v, map[string]interface{}{"Flash": err.Error(), "FlashType": "error"})
}

func (s *Server) handlePortForwardAdd(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pf, err := formForward(r)
	if err == nil {
		_, _, err = s.addForward(r, name, pf)
	}
	if err != nil {
		s.renderForwardError(w, r, name, err)
		return
	}
	http.Redirect(w, r, returnPath(r, name), http.StatusSeeOther)
}

func (s *Server) handlePortForwardRemove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pf, err := formForward(r)
	if err == nil {
		_, err = s.removeForward(r, name, pf.HostPort, pf.GuestPort, pf.Protocol)
	}
	if err != nil {
		s.renderForwardError(w, r, name, err)
		return
	}
	http.Redirect(w, r, returnPath(r, name), http.StatusSeeOther)
}

// handlePortForwardApply re-installs a running VM's forwards, for ones shown
// as missing or outdated (left by an older vmm).
func (s *Server) handlePortForwardApply(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	v, err := vm.Load(s.cfg.GetPaths().VMs, name)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}
	firecracker.NewClient().UpdateVMState(v)
	if v.State != vm.StateRunning {
		s.renderForwardError(w, r, name, errors.New("VM is not running; its forwards apply when it starts"))
		return
	}
	if err := portforward.Apply(s.netManager(), v); err != nil {
		s.renderForwardError(w, r, name, err)
		return
	}
	s.sseBroker.Note(kindVM, name, "port-forward", requestSource(r), "re-applied forwards")
	http.Redirect(w, r, returnPath(r, name), http.StatusSeeOther)
}

// JSON API

func (s *Server) handleAPIPortForwards(w http.ResponseWriter, r *http.Request) {
	all, err := loadAllVMs(s.cfg.GetPaths().VMs)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries := portforward.List(s.netManager(), all)
	if entries == nil {
		entries = []portforward.Entry{}
	}
	jsonResponse(w, entries)
}

func (s *Server) handleAPIVMPortForwards(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	v, err := vm.Load(s.cfg.GetPaths().VMs, name)
	if err != nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}
	firecracker.NewClient().UpdateVMState(v)
	entries := portforward.List(s.netManager(), []*vm.VM{v})
	if entries == nil {
		entries = []portforward.Entry{}
	}
	jsonResponse(w, entries)
}

func (s *Server) handleAPIPortForwardAdd(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	var pf vm.PortForward
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&pf); err != nil {
		jsonError(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if pf.Protocol == "" {
		pf.Protocol = "tcp"
	}
	if err := portforward.Validate(pf); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	live, code, err := s.addForward(r, name, pf)
	if err != nil {
		jsonError(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"host_port": pf.HostPort, "guest_port": pf.GuestPort, "protocol": pf.Protocol, "live": live,
	}); err != nil {
		log.Printf("port-forward add response: %v", err)
	}
}

func (s *Server) handleAPIPortForwardRemove(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	hostPort, err1 := strconv.Atoi(q.Get("host_port"))
	guestPort, err2 := strconv.Atoi(q.Get("guest_port"))
	if err1 != nil || err2 != nil {
		jsonError(w, "host_port and guest_port query parameters are required", http.StatusBadRequest)
		return
	}
	protocol := q.Get("protocol")
	if protocol != "" {
		if err := network.ValidateProtocol(protocol); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if code, err := s.removeForward(r, name, hostPort, guestPort, protocol); err != nil {
		jsonError(w, err.Error(), code)
		return
	}
	jsonResponse(w, map[string]string{"status": "removed"})
}
