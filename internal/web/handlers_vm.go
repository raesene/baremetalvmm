package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/network"
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

	fcClient := firecracker.NewClient()
	for _, v := range vms {
		fcClient.UpdateVMState(v)
	}

	s.renderPage(w, r, "vms.html", "vms", map[string]interface{}{
		"VMs": vms,
	})
}

func (s *Server) handleVMCreateForm(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	kernels, _ := imgMgr.ListKernels()
	images, _ := imgMgr.ListRootfs()
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

	var dnsServers []string
	if dnsStr != "" {
		for _, d := range strings.Split(dnsStr, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				dnsServers = append(dnsServers, d)
			}
		}
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

	newVM := vm.NewVM(name)
	newVM.CPUs = cpus
	newVM.MemoryMB = memory
	newVM.DiskSizeMB = disk
	newVM.Image = imageName
	newVM.Kernel = kernelName
	newVM.MacAddress = newVM.GenerateMacAddress()
	newVM.TapDevice = network.GenerateTapName(newVM.ID)
	newVM.DNSServers = dnsServers
	newVM.SSHPublicKey = sshKey
	newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, name)

	if err := newVM.Save(paths.VMs); err != nil {
		s.renderPage(w, r, "vm_create.html", "vms", map[string]interface{}{
			"Flash":     "Failed to create VM: " + err.Error(),
			"FlashType": "error",
		})
		return
	}

	http.Redirect(w, r, "/vms", http.StatusSeeOther)
}

func (s *Server) handleVMDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		http.Error(w, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(v)

	s.renderPage(w, r, "vm_detail.html", "vms", map[string]interface{}{
		"VM": v,
	})
}

func (s *Server) handleVMStart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(existingVM)

	if existingVM.State == vm.StateRunning {
		httpError(w, r, "VM is already running", http.StatusConflict)
		return
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	if err := imgMgr.EnsureDefaultImages(); err != nil {
		httpError(w, r, "Failed to ensure images: "+err.Error(), http.StatusInternalServerError)
		return
	}

	vmRootfs, err := imgMgr.CreateVMRootfs(name, paths.VMs, existingVM.DiskSizeMB, existingVM.Image)
	if err != nil {
		httpError(w, r, "Failed to create VM rootfs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	existingVM.RootfsPath = vmRootfs
	existingVM.KernelPath = imgMgr.GetKernelPath(existingVM.Kernel)

	if existingVM.SSHPublicKey != "" {
		if err := image.InjectSSHKey(existingVM.RootfsPath, existingVM.SSHPublicKey); err != nil {
			httpError(w, r, "Failed to inject SSH key: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := image.InjectDNSConfig(existingVM.RootfsPath, existingVM.DNSServers); err != nil {
		httpError(w, r, "Failed to inject DNS config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if err := netMgr.EnsureBridge(); err != nil {
		httpError(w, r, "Failed to setup bridge: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if !netMgr.TapExists(existingVM.TapDevice) {
		if err := netMgr.CreateTap(existingVM.TapDevice); err != nil {
			httpError(w, r, "Failed to create TAP device: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	ip, err := netMgr.AllocateIP(usedVMIPs(paths.VMs))
	if err != nil {
		httpError(w, r, "Failed to allocate IP: "+err.Error(), http.StatusInternalServerError)
		return
	}
	existingVM.IPAddress = ip

	existingVM.State = vm.StateStarting
	existingVM.Save(paths.VMs)

	ctx := context.Background()
	vmCfg := &firecracker.VMConfig{
		SocketPath: existingVM.SocketPath,
		KernelPath: existingVM.KernelPath,
		RootfsPath: existingVM.RootfsPath,
		CPUs:       existingVM.CPUs,
		MemoryMB:   existingVM.MemoryMB,
		TapDevice:  existingVM.TapDevice,
		MacAddress: existingVM.MacAddress,
		LogPath:    fmt.Sprintf("%s/%s.log", paths.Logs, name),
		IPAddress:  existingVM.IPAddress,
		Gateway:    s.cfg.Gateway,
	}

	machine, err := fcClient.StartVM(ctx, vmCfg)
	if err != nil {
		existingVM.State = vm.StateError
		existingVM.Save(paths.VMs)
		httpError(w, r, "Failed to start VM: "+err.Error(), http.StatusInternalServerError)
		return
	}

	existingVM.State = vm.StateRunning
	existingVM.PID = fcClient.GetVMPID(machine)
	existingVM.StartedAt = time.Now()
	existingVM.Save(paths.VMs)

	if isHTMXRequest(r) {
		s.renderVMRow(w, existingVM)
	} else {
		http.Redirect(w, r, "/vms", http.StatusSeeOther)
	}
}

func (s *Server) handleVMStop(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(existingVM)

	if existingVM.State != vm.StateRunning {
		httpError(w, r, "VM is not running", http.StatusConflict)
		return
	}

	existingVM.State = vm.StateStopping
	existingVM.Save(paths.VMs)

	ctx := context.Background()
	if err := fcClient.StopVM(ctx, existingVM.SocketPath); err != nil {
		if existingVM.PID > 0 {
			if proc, err := os.FindProcess(existingVM.PID); err == nil {
				proc.Signal(syscall.SIGKILL)
			}
		}
	}

	time.Sleep(500 * time.Millisecond)

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
		netMgr.DeleteTap(existingVM.TapDevice)
	}

	existingVM.State = vm.StateStopped
	existingVM.PID = 0
	existingVM.Save(paths.VMs)
	os.Remove(existingVM.SocketPath)

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
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		httpError(w, r, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(existingVM)

	if existingVM.State == vm.StateRunning {
		ctx := context.Background()
		fcClient.StopVM(ctx, existingVM.SocketPath)
		if existingVM.PID > 0 {
			if proc, err := os.FindProcess(existingVM.PID); err == nil {
				proc.Signal(syscall.SIGKILL)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
		netMgr.DeleteTap(existingVM.TapDevice)
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	imgMgr.DeleteVMRootfs(name, paths.VMs)
	os.Remove(existingVM.SocketPath)
	vm.Delete(paths.VMs, name)

	if isHTMXRequest(r) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(""))
	} else {
		http.Redirect(w, r, "/vms", http.StatusSeeOther)
	}
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

	fcClient := firecracker.NewClient()
	for _, v := range vms {
		fcClient.UpdateVMState(v)
	}

	jsonResponse(w, vms)
}

func (s *Server) handleAPIVMDetail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(v)

	jsonResponse(w, v)
}

func (s *Server) handleAPIVMCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string   `json:"name"`
		CPUs       int      `json:"cpus"`
		MemoryMB   int      `json:"memory_mb"`
		DiskSizeMB int      `json:"disk_size_mb"`
		SSHKey     string   `json:"ssh_key"`
		Kernel     string   `json:"kernel"`
		Image      string   `json:"image"`
		DNSServers []string `json:"dns_servers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		jsonError(w, "Name is required", http.StatusBadRequest)
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

	paths := s.cfg.GetPaths()
	s.cfg.EnsureDirectories()

	if vm.Exists(paths.VMs, req.Name) {
		jsonError(w, fmt.Sprintf("VM '%s' already exists", req.Name), http.StatusConflict)
		return
	}

	newVM := vm.NewVM(req.Name)
	newVM.CPUs = req.CPUs
	newVM.MemoryMB = req.MemoryMB
	newVM.DiskSizeMB = req.DiskSizeMB
	newVM.Image = req.Image
	newVM.Kernel = req.Kernel
	newVM.MacAddress = newVM.GenerateMacAddress()
	newVM.TapDevice = network.GenerateTapName(newVM.ID)
	newVM.DNSServers = req.DNSServers
	newVM.SSHPublicKey = req.SSHKey
	newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, req.Name)

	if err := newVM.Save(paths.VMs); err != nil {
		jsonError(w, "Failed to create VM: "+err.Error(), http.StatusInternalServerError)
		return
	}

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
	paths := s.cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(existingVM)

	if existingVM.State == vm.StateRunning {
		ctx := context.Background()
		fcClient.StopVM(ctx, existingVM.SocketPath)
		if existingVM.PID > 0 {
			if proc, err := os.FindProcess(existingVM.PID); err == nil {
				proc.Signal(syscall.SIGKILL)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
		netMgr.DeleteTap(existingVM.TapDevice)
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	imgMgr.DeleteVMRootfs(name, paths.VMs)
	os.Remove(existingVM.SocketPath)
	vm.Delete(paths.VMs, name)

	jsonResponse(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, map[string]string{"status": "ok"})
}

// helpers

func usedVMIPs(vmsDir string) []string {
	vms, _ := vm.List(vmsDir)
	var ips []string
	for _, v := range vms {
		if v.IPAddress != "" {
			ips = append(ips, v.IPAddress)
		}
	}
	return ips
}

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
