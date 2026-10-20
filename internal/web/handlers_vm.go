package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/lifecycle"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/portforward"
	"github.com/raesene/baremetalvmm/internal/snapshot"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
)

func (s *Server) handleVMList(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	vms, err := vm.List(paths.VMs)
	if err != nil {
		s.renderPage(w, r, "vms.html", "vms", map[string]interface{}{
			"Flash":     "Failed to list VMs: " + err.Error(),
			"FlashType": "error",
		})
		return
	}

	for _, v := range vms {
		hypervisor.UpdateVMState(v)
	}

	s.renderPage(w, r, "vms.html", "vms", map[string]interface{}{
		"VMs": vms,
	})
}

func (s *Server) handleVMCreateForm(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	kernels, _ := imgMgr.ListKernelsWithInfo()
	images, _ := imgMgr.ListRootfsWithInfo()
	defaults := s.cfg.GetVMDefaults()

	sshKey := ""
	if defaults.SSHKeyPath != "" {
		keyPath := expandHomePath(defaults.SSHKeyPath)
		if data, err := os.ReadFile(keyPath); err == nil {
			sshKey = string(data)
		}
	}

	cpus := defaults.CPUs
	if cpus == 0 {
		cpus = 1
	}
	memoryMB := defaults.MemoryMB
	if memoryMB == 0 {
		memoryMB = 512
	}
	diskMB := defaults.DiskSizeMB
	if diskMB == 0 {
		diskMB = 1024
	}

	s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
		"Kernels": kernels,
		"Images":  images,
		"Defaults": map[string]interface{}{
			"CPUs":       cpus,
			"MemoryMB":   memoryMB,
			"DiskSizeMB": diskMB,
			"SSHKey":     sshKey,
			"Kernel":     defaults.Kernel,
			"Image":      defaults.Image,
		},
	})
}

func (s *Server) handleVMCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     "VM name is required",
			"FlashType": "error",
		})
		return
	}
	if err := validate.VMName(name); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     err.Error(),
			"FlashType": "error",
		})
		return
	}

	paths := s.cfg.GetPaths()
	if err := s.cfg.EnsureDirectories(); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     "Failed to create directories: " + err.Error(),
			"FlashType": "error",
		})
		return
	}

	if vm.Exists(paths.VMs, name) {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     fmt.Sprintf("VM '%s' already exists", name),
			"FlashType": "error",
		})
		return
	}

	cpus := formInt(r, "cpus", 1)
	memory := formInt(r, "memory", 512)
	disk := formInt(r, "disk", 1024)
	sshKey := strings.TrimSpace(r.FormValue("ssh_key"))
	kernelName := r.FormValue("kernel")
	imageName := r.FormValue("image")
	dnsStr := strings.TrimSpace(r.FormValue("dns"))

	if err := validate.CPUs(cpus); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}
	if err := validate.MemoryMB(memory); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}
	if err := validate.DiskSizeMB(disk); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}

	var dnsServers []string
	if dnsStr != "" {
		for _, d := range strings.Split(dnsStr, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				dnsServers = append(dnsServers, d)
			}
		}
	}
	for _, dns := range dnsServers {
		if err := validate.DNSServer(dns); err != nil {
			s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
				"Flash": err.Error(), "FlashType": "error",
			})
			return
		}
	}

	var portForwards []vm.PortForward
	hostPorts := r.Form["host_port"]
	guestPorts := r.Form["guest_port"]
	protocols := r.Form["protocol"]
	for i := range hostPorts {
		if hostPorts[i] == "" || guestPorts[i] == "" {
			continue
		}
		hp := formIntFromString(hostPorts[i], 0)
		gp := formIntFromString(guestPorts[i], 0)
		if hp <= 0 || hp > 65535 || gp <= 0 || gp > 65535 {
			continue
		}
		proto := "tcp"
		if i < len(protocols) && protocols[i] == "udp" {
			proto = "udp"
		}
		portForwards = append(portForwards, vm.PortForward{
			HostPort:  hp,
			GuestPort: gp,
			Protocol:  proto,
		})
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	if imageName != "" && !imgMgr.ImageExists(imageName) {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     fmt.Sprintf("Image '%s' not found", imageName),
			"FlashType": "error",
		})
		return
	}
	if kernelName != "" && !imgMgr.KernelExists(kernelName) {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     fmt.Sprintf("Kernel '%s' not found", kernelName),
			"FlashType": "error",
		})
		return
	}

	nested := r.FormValue("nested") != ""
	backend, kernelName, err := lifecycle.ChooseBackend(imgMgr, "", nested, kernelName)
	if err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}

	restrictLocal := r.FormValue("restrict_local_networks") != ""
	if restrictLocal {
		if err := lifecycle.CheckRestrictedDNS(dnsServers); err != nil {
			s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
				"Flash": err.Error(), "FlashType": "error",
			})
			return
		}
	}

	if all, err := vm.List(paths.VMs); err == nil {
		if err := portforward.CheckNew(all, name, portForwards); err != nil {
			s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
				"Flash": err.Error(), "FlashType": "error",
			})
			return
		}
	}

	newVM := vm.NewVM(name)
	newVM.CPUs = cpus
	newVM.MemoryMB = memory
	newVM.DiskSizeMB = disk
	newVM.Image = imageName
	newVM.Kernel = kernelName
	newVM.Backend = backend
	newVM.Nested = nested
	newVM.RestrictLocalNetworks = restrictLocal
	newVM.MacAddress = newVM.GenerateMacAddress()
	newVM.TapDevice = network.GenerateTapName(newVM.ID)
	newVM.DNSServers = dnsServers
	newVM.SSHPublicKey = sshKey
	newVM.PortForwards = portForwards
	newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, name)

	if err := newVM.Save(paths.VMs); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     "Failed to create VM: " + err.Error(),
			"FlashType": "error",
		})
		return
	}
	s.recordVMCreated(newVM, requestSource(r))

	http.Redirect(w, r, "/vms", http.StatusSeeOther)
}

func (s *Server) handleVMDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(v)
	s.renderVMDetail(w, r, v, nil)
}

// renderVMDetail renders a VM's page. v must have its current state; data
// may carry a Flash message.
func (s *Server) renderVMDetail(w http.ResponseWriter, r *http.Request, v *vm.VM, data map[string]interface{}) {
	paths := s.cfg.GetPaths()
	snapMgr := snapshot.NewManager(paths.Snapshots)
	snaps, err := snapMgr.List(v.Name)
	if err != nil {
		log.Printf("failed to list snapshots for VM %s: %v", v.Name, err)
	}

	if data == nil {
		data = map[string]interface{}{}
	}
	data["VM"] = v
	data["Backend"] = hypervisor.Name(v)
	_, snapErr := hypervisor.SnapshotterFor(v)
	data["CanSnapshot"] = snapErr == nil
	data["Snapshots"] = snaps
	data["MaxUpload"] = s.maxUploadBytes
	s.addForwardRows(data, []*vm.VM{v})
	s.renderPage(w, r, "vm_detail.html", "vms", data)
}

func (s *Server) startVM(existingVM *vm.VM) error {
	return lifecycle.Start(s.cfg, existingVM, lifecycle.Options{
		Warn: func(msg string) { log.Printf("vm %s: %s", existingVM.Name, msg) },
	})
}

func (s *Server) handleVMStart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(existingVM)

	if existingVM.State == vm.StateRunning {
		httpError(w, r, "VM is already running", http.StatusConflict)
		return
	}

	if err := s.startVMTracked(existingVM, requestSource(r)); err != nil {
		httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}

	if isHTMXRequest(r) {
		s.renderVMRow(w, existingVM)
	} else {
		http.Redirect(w, r, "/vms", http.StatusSeeOther)
	}
}

func (s *Server) handleVMStop(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(existingVM)

	if existingVM.State != vm.StateRunning {
		httpError(w, r, "VM is not running", http.StatusConflict)
		return
	}

	source := requestSource(r)
	release := s.sseBroker.Hold(kindVM, name)
	defer release()

	existingVM.State = vm.StateStopping
	existingVM.Save(paths.VMs)
	s.sseBroker.Record(kindVM, name, string(vm.StateStopping), source, "")

	ctx := context.Background()
	if err := hypervisor.Terminate(ctx, existingVM); err != nil {
		existingVM.State = vm.StateError
		if saveErr := existingVM.Save(paths.VMs); saveErr != nil {
			log.Printf("failed to save state for VM %s: %v", name, saveErr)
		}
		s.sseBroker.Record(kindVM, name, string(vm.StateError), source, "stop: "+err.Error())
		httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
		netMgr.DeleteTap(existingVM.TapDevice)
	}

	if err := portforward.Clear(netMgr, existingVM); err != nil {
		log.Printf("vm %s: failed to remove port forwards: %v", existingVM.Name, err)
	}

	// Terminate already cleared the PID and removed the socket
	existingVM.State = vm.StateStopped
	existingVM.Save(paths.VMs)
	s.sseBroker.Record(kindVM, name, string(vm.StateStopped), source, "")

	if isHTMXRequest(r) {
		s.renderVMRow(w, existingVM)
	} else {
		http.Redirect(w, r, "/vms", http.StatusSeeOther)
	}
}

func (s *Server) handleVMDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteVM(w, r)
}

func (s *Server) handleVMDeletePost(w http.ResponseWriter, r *http.Request) {
	s.deleteVM(w, r)
}

func (s *Server) deleteVM(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(existingVM)

	source := requestSource(r)
	release := s.sseBroker.Hold(kindVM, name)
	defer release()

	if existingVM.State == vm.StateRunning {
		ctx := context.Background()
		// Never remove the VM record while its process survives
		if err := hypervisor.Terminate(ctx, existingVM); err != nil {
			existingVM.State = vm.StateError
			if saveErr := existingVM.Save(paths.VMs); saveErr != nil {
				log.Printf("failed to save state for VM %s: %v", name, saveErr)
			}
			s.sseBroker.Note(kindVM, name, "failed", source, "delete: "+err.Error())
			httpError(w, r, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
		netMgr.DeleteTap(existingVM.TapDevice)
	}

	if err := portforward.Clear(netMgr, existingVM); err != nil {
		log.Printf("vm %s: failed to remove port forwards: %v", existingVM.Name, err)
	}
	if existingVM.RestrictLocalNetworks && existingVM.IPAddress != "" {
		if err := netMgr.SetLocalNetworkRestriction(false, existingVM.TapDevice, existingVM.MacAddress, existingVM.IPAddress); err != nil {
			log.Printf("vm %s: failed to remove local network restriction: %v", existingVM.Name, err)
		}
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	imgMgr.DeleteVMRootfs(name, paths.VMs)
	if err := snapshot.NewManager(paths.Snapshots).DeleteAllForVM(name); err != nil {
		log.Printf("vm %s: failed to delete snapshots: %v", name, err)
	}
	os.Remove(existingVM.SocketPath)
	vm.Delete(paths.VMs, name)
	s.sseBroker.Record(kindVM, name, stateDeleted, source, "")

	if isHTMXRequest(r) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(""))
	} else {
		http.Redirect(w, r, "/vms", http.StatusSeeOther)
	}
}

// recordVMCreated logs a newly defined VM in the activity stream.
func (s *Server) recordVMCreated(v *vm.VM, source string) {
	s.sseBroker.Record(kindVM, v.Name, string(v.State), source, fmt.Sprintf("%d vCPU · %d MB", v.CPUs, v.MemoryMB))
}

// startVMTracked starts a VM and logs the transition as a console action.
func (s *Server) startVMTracked(v *vm.VM, source string) error {
	release := s.sseBroker.Hold(kindVM, v.Name)
	defer release()
	s.sseBroker.Record(kindVM, v.Name, string(vm.StateStarting), source, "")
	if err := s.startVM(v); err != nil {
		s.sseBroker.Note(kindVM, v.Name, "failed", source, "start: "+err.Error())
		return err
	}
	s.sseBroker.Record(kindVM, v.Name, string(v.State), source, v.IPAddress)
	return nil
}

func (s *Server) renderVMRow(w http.ResponseWriter, v *vm.VM) {
	if t, ok := s.templates["vm_row.html"]; ok {
		t.ExecuteTemplate(w, "vm_row.html", v)
	}
}

// JSON API handlers

func (s *Server) handleAPIVMList(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	vms, err := vm.List(paths.VMs)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, v := range vms {
		hypervisor.UpdateVMState(v)
	}

	jsonResponse(w, vms)
}

func (s *Server) handleAPIVMDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(v)

	jsonResponse(w, v)
}

func (s *Server) handleAPIVMCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string           `json:"name"`
		CPUs         int              `json:"cpus"`
		MemoryMB     int              `json:"memory_mb"`
		DiskSizeMB   int              `json:"disk_size_mb"`
		SSHKey       string           `json:"ssh_key"`
		Kernel       string           `json:"kernel"`
		Image        string           `json:"image"`
		DNSServers   []string         `json:"dns_servers"`
		PortForwards []vm.PortForward `json:"port_forwards"`
		Backend      string           `json:"backend"`
		Nested       bool             `json:"nested"`

		RestrictLocalNetworks bool `json:"restrict_local_networks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		jsonError(w, "Name is required", http.StatusBadRequest)
		return
	}
	if err := validate.VMName(req.Name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.CPUs == 0 {
		req.CPUs = 1
	}
	if req.MemoryMB == 0 {
		req.MemoryMB = 512
	}
	if req.DiskSizeMB == 0 {
		req.DiskSizeMB = 1024
	}

	if err := validate.CPUs(req.CPUs); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validate.MemoryMB(req.MemoryMB); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validate.DiskSizeMB(req.DiskSizeMB); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, dns := range req.DNSServers {
		if err := validate.DNSServer(dns); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.RestrictLocalNetworks {
		if err := lifecycle.CheckRestrictedDNS(req.DNSServers); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	paths := s.cfg.GetPaths()
	s.cfg.EnsureDirectories()

	if vm.Exists(paths.VMs, req.Name) {
		jsonError(w, fmt.Sprintf("VM '%s' already exists", req.Name), http.StatusConflict)
		return
	}

	all, err := vm.List(paths.VMs)
	if err != nil {
		jsonError(w, "Failed to list VMs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := portforward.CheckNew(all, req.Name, req.PortForwards); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	backend, kernelName, err := lifecycle.ChooseBackend(imgMgr, req.Backend, req.Nested, req.Kernel)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	newVM := vm.NewVM(req.Name)
	newVM.CPUs = req.CPUs
	newVM.MemoryMB = req.MemoryMB
	newVM.DiskSizeMB = req.DiskSizeMB
	newVM.Image = req.Image
	newVM.Kernel = kernelName
	newVM.Backend = backend
	newVM.Nested = req.Nested
	newVM.RestrictLocalNetworks = req.RestrictLocalNetworks
	newVM.MacAddress = newVM.GenerateMacAddress()
	newVM.TapDevice = network.GenerateTapName(newVM.ID)
	newVM.DNSServers = req.DNSServers
	newVM.SSHPublicKey = req.SSHKey
	newVM.PortForwards = req.PortForwards
	newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, req.Name)

	if err := newVM.Save(paths.VMs); err != nil {
		jsonError(w, "Failed to create VM: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.recordVMCreated(newVM, requestSource(r))

	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, newVM)
}

func (s *Server) handleAPIVMStart(w http.ResponseWriter, r *http.Request) {
	s.handleVMStart(w, r)
}

func (s *Server) handleAPIVMStop(w http.ResponseWriter, r *http.Request) {
	s.handleVMStop(w, r)
}

func (s *Server) handleAPIVMDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	hypervisor.UpdateVMState(existingVM)

	source := requestSource(r)
	release := s.sseBroker.Hold(kindVM, name)
	defer release()

	if existingVM.State == vm.StateRunning {
		ctx := context.Background()
		// Never remove the VM record while its process survives
		if err := hypervisor.Terminate(ctx, existingVM); err != nil {
			existingVM.State = vm.StateError
			if saveErr := existingVM.Save(paths.VMs); saveErr != nil {
				log.Printf("failed to save state for VM %s: %v", name, saveErr)
			}
			s.sseBroker.Note(kindVM, name, "failed", source, "delete: "+err.Error())
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
		netMgr.DeleteTap(existingVM.TapDevice)
	}

	if err := portforward.Clear(netMgr, existingVM); err != nil {
		log.Printf("vm %s: failed to remove port forwards: %v", existingVM.Name, err)
	}
	if existingVM.RestrictLocalNetworks && existingVM.IPAddress != "" {
		if err := netMgr.SetLocalNetworkRestriction(false, existingVM.TapDevice, existingVM.MacAddress, existingVM.IPAddress); err != nil {
			log.Printf("vm %s: failed to remove local network restriction: %v", existingVM.Name, err)
		}
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	imgMgr.DeleteVMRootfs(name, paths.VMs)
	if err := snapshot.NewManager(paths.Snapshots).DeleteAllForVM(name); err != nil {
		log.Printf("vm %s: failed to delete snapshots: %v", name, err)
	}
	os.Remove(existingVM.SocketPath)
	vm.Delete(paths.VMs, name)
	s.sseBroker.Record(kindVM, name, stateDeleted, source, "")

	jsonResponse(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, map[string]string{"status": "ok"})
}

// helpers

func expandHomePath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		home, _ := os.UserHomeDir()
		if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
			home = "/home/" + sudoUser
		}
		return home + path[1:]
	}
	return path
}

func formInt(r *http.Request, name string, defaultVal int) int {
	s := r.FormValue(name)
	if s == "" {
		return defaultVal
	}
	return formIntFromString(s, defaultVal)
}

func formIntFromString(s string, defaultVal int) int {
	var v int
	fmt.Sscanf(s, "%d", &v)
	if v <= 0 {
		return defaultVal
	}
	return v
}

func httpError(w http.ResponseWriter, r *http.Request, msg string, code int) {
	if isAPIRequest(r) || isHTMXRequest(r) {
		if isAPIRequest(r) {
			jsonError(w, msg, code)
		} else {
			http.Error(w, msg, code)
		}
	} else {
		http.Error(w, msg, code)
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
