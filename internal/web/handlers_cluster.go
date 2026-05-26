package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
)

func (s *Server) handleClusterList(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	clusters, err := cluster.List(paths.Clusters)
	if err != nil {
		s.renderPage(w, r, "clusters.html", "clusters", map[string]interface{}{
			"Flash":     "Failed to list clusters: " + err.Error(),
			"FlashType": "error",
		})
		return
	}

	s.renderPage(w, r, "clusters.html", "clusters", map[string]interface{}{
		"Clusters": clusters,
	})
}

func (s *Server) handleClusterCreateForm(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	kernels, _ := imgMgr.ListKernelsWithInfo()
	images, _ := imgMgr.ListRootfsWithInfo()

	var k8sImages []image.RootfsInfo
	for _, img := range images {
		if strings.HasPrefix(img.Name, "k8s-") {
			k8sImages = append(k8sImages, img)
		}
	}
	hasSecurityImage := imgMgr.FindSecurityRootfs() != ""
	defaults := s.cfg.GetVMDefaults()

	sshKey := ""
	sshKeyPath := ""
	if defaults.SSHKeyPath != "" {
		keyPath := expandHomePath(defaults.SSHKeyPath)
		if data, err := os.ReadFile(keyPath); err == nil {
			sshKey = string(data)
		}
		sshKeyPath = strings.TrimSuffix(defaults.SSHKeyPath, ".pub")
	}

	s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
		"Kernels":          kernels,
		"K8sImages":        k8sImages,
		"HasSecurityImage": hasSecurityImage,
		"Defaults": map[string]interface{}{
			"SSHKey":     sshKey,
			"SSHKeyPath": sshKeyPath,
		},
		"DefaultCPUs":   2,
		"DefaultMemory": 4096,
	})
}

func (s *Server) handleClusterCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     "Cluster name is required",
			"FlashType": "error",
		})
		return
	}
	if err := validate.ClusterName(name); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     err.Error(),
			"FlashType": "error",
		})
		return
	}

	paths := s.cfg.GetPaths()
	if err := s.cfg.EnsureDirectories(); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     "Failed to create directories: " + err.Error(),
			"FlashType": "error",
		})
		return
	}

	if cluster.Exists(paths.Clusters, name) {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     fmt.Sprintf("Cluster '%s' already exists", name),
			"FlashType": "error",
		})
		return
	}

	workers := formInt(r, "workers", 0)
	cpus := formInt(r, "cpus", 2)
	memory := formInt(r, "memory", 4096)
	disk := formInt(r, "disk", 10240)
	sshKey := strings.TrimSpace(r.FormValue("ssh_key"))
	sshKeyPath := strings.TrimSpace(r.FormValue("ssh_key_path"))
	kernelName := r.FormValue("kernel")
	imageName := r.FormValue("image")
	adminWorkstation := r.FormValue("admin_workstation") == "on"

	if err := validate.CPUs(cpus); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}
	if err := validate.MemoryMB(memory); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}
	if err := validate.DiskSizeMB(disk); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}

	k8sVersion := strings.TrimPrefix(imageName, "k8s-")
	if imageName == "" || k8sVersion == "" {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     "A Kubernetes rootfs image must be selected",
			"FlashType": "error",
		})
		return
	}
	if err := validate.K8sVersion(k8sVersion); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash": err.Error(), "FlashType": "error",
		})
		return
	}

	if sshKeyPath == "" {
		if err := sshkey.EnsureKeyPair(paths.SSH); err != nil {
			s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
				"Flash":     "Failed to ensure vmm SSH key: " + err.Error(),
				"FlashType": "error",
			})
			return
		}
		sshKeyPath = sshkey.PrivateKeyPath(paths.SSH)
	}

	if cpus < 2 {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     "Kubernetes requires at least 2 CPUs",
			"FlashType": "error",
		})
		return
	}

	cl := cluster.NewCluster(name, workers, k8sVersion)
	cl.CPUs = cpus
	cl.MemoryMB = memory
	cl.DiskSizeMB = disk
	cl.Image = imageName
	cl.Kernel = kernelName
	cl.SSHKeyPath = sshKeyPath

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	// Default to k8s-kernel if available
	if kernelName == "" && imgMgr.KernelExists("k8s-kernel") {
		cl.Kernel = "k8s-kernel"
	}

	if adminWorkstation {
		secImage := imgMgr.FindSecurityRootfs()
		if secImage == "" {
			s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
				"Flash":     "Admin workstation requires a security-* rootfs image (none found)",
				"FlashType": "error",
			})
			return
		}
		cl.AdminVM = fmt.Sprintf("%s-admin", name)
	}

	if err := cl.Save(paths.Clusters); err != nil {
		s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
			"Flash":     "Failed to save cluster config: " + err.Error(),
			"FlashType": "error",
		})
		return
	}

	// Create VMs for the cluster
	allVMs := cl.AllVMs()
	for _, vmName := range allVMs {
		newVM := vm.NewVM(vmName)
		if vmName == cl.AdminVM {
			secImage := imgMgr.FindSecurityRootfs()
			newVM.CPUs = 2
			newVM.MemoryMB = 4096
			newVM.DiskSizeMB = 20480
			newVM.Image = secImage
			newVM.Kernel = ""
		} else {
			newVM.CPUs = cl.CPUs
			newVM.MemoryMB = cl.MemoryMB
			newVM.DiskSizeMB = cl.DiskSizeMB
			newVM.Image = cl.Image
			newVM.Kernel = cl.Kernel
		}
		newVM.MacAddress = newVM.GenerateMacAddress()
		newVM.TapDevice = network.GenerateTapName(newVM.ID)
		newVM.SSHPublicKey = sshKey
		newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, vmName)

		if err := newVM.Save(paths.VMs); err != nil {
			cl.State = cluster.StateError
			cl.Save(paths.Clusters)
			s.renderPage(w, r, "cluster_create.html", "clusters", map[string]interface{}{
				"Flash":     fmt.Sprintf("Failed to create VM '%s': %s", vmName, err.Error()),
				"FlashType": "error",
			})
			return
		}
	}

	go s.provisionClusterInBackground(name)

	http.Redirect(w, r, "/clusters", http.StatusSeeOther)
}

func (s *Server) provisionClusterInBackground(clusterName string) {
	paths := s.cfg.GetPaths()

	cl, err := cluster.Load(paths.Clusters, clusterName)
	if err != nil {
		log.Printf("cluster %s: failed to load config: %v", clusterName, err)
		return
	}

	sshKeyPath := expandHomePath(cl.SSHKeyPath)

	// Start all VMs sequentially
	log.Printf("cluster %s: starting VMs...", clusterName)
	var nodeInfos []cluster.NodeInfo
	var adminIP string
	for _, vmName := range cl.AllVMs() {
		existingVM, err := vm.Load(paths.VMs, vmName)
		if err != nil {
			log.Printf("cluster %s: failed to load VM %s: %v", clusterName, vmName, err)
			cl.SetError(fmt.Sprintf("failed to load VM %s: %v", vmName, err))
			cl.Save(paths.Clusters)
			return
		}

		fcClient := firecracker.NewClient()
		fcClient.UpdateVMState(existingVM)
		if existingVM.State == vm.StateRunning {
			if vmName == cl.AdminVM {
				adminIP = existingVM.IPAddress
			} else {
				nodeInfos = append(nodeInfos, cluster.NodeInfo{Name: vmName, IP: existingVM.IPAddress})
			}
			continue
		}

		if err := s.startVM(existingVM); err != nil {
			log.Printf("cluster %s: failed to start VM %s: %v", clusterName, vmName, err)
			cl.SetError(fmt.Sprintf("failed to start VM %s: %v", vmName, err))
			cl.Save(paths.Clusters)
			return
		}
		log.Printf("cluster %s: started VM %s (%s)", clusterName, vmName, existingVM.IPAddress)
		if vmName == cl.AdminVM {
			adminIP = existingVM.IPAddress
		} else {
			nodeInfos = append(nodeInfos, cluster.NodeInfo{Name: vmName, IP: existingVM.IPAddress})
		}
	}

	// Provision Kubernetes (admin VM excluded)
	log.Printf("cluster %s: provisioning Kubernetes...", clusterName)
	if err := cluster.ProvisionCluster(cl, sshKeyPath, nodeInfos); err != nil {
		log.Printf("cluster %s: provisioning failed: %v", clusterName, err)
		cl.SetError(fmt.Sprintf("provisioning failed: %v", err))
		cl.Save(paths.Clusters)
		return
	}

	// Extract and merge kubeconfig
	log.Printf("cluster %s: extracting kubeconfig...", clusterName)
	cpClient, err := cluster.WaitForSSH(cl.ControlPlaneIP, sshKeyPath, 30*time.Second)
	if err != nil {
		log.Printf("cluster %s: failed to connect for kubeconfig: %v", clusterName, err)
		cl.SetError(fmt.Sprintf("failed to connect for kubeconfig: %v", err))
		cl.Save(paths.Clusters)
		return
	}
	defer cpClient.Close()

	kubeconfigYAML, err := cluster.ExtractKubeconfig(cpClient)
	if err != nil {
		log.Printf("cluster %s: failed to extract kubeconfig: %v", clusterName, err)
		cl.SetError(fmt.Sprintf("failed to extract kubeconfig: %v", err))
		cl.Save(paths.Clusters)
		return
	}

	if err := cluster.MergeKubeconfig(clusterName, kubeconfigYAML); err != nil {
		log.Printf("cluster %s: failed to merge kubeconfig: %v", clusterName, err)
		cl.SetError(fmt.Sprintf("failed to merge kubeconfig: %v", err))
		cl.Save(paths.Clusters)
		return
	}

	// Copy kubeconfig to admin workstation
	if cl.AdminVM != "" && adminIP != "" {
		log.Printf("cluster %s: copying kubeconfig to admin workstation %s...", clusterName, cl.AdminVM)
		if err := cluster.CopyKubeconfigToVM(adminIP, sshKeyPath, kubeconfigYAML, cl.ControlPlaneIP); err != nil {
			log.Printf("cluster %s: warning: failed to copy kubeconfig to admin workstation: %v", clusterName, err)
		} else {
			log.Printf("cluster %s: kubeconfig copied to admin workstation", clusterName)
		}
	}

	cl.State = cluster.StateRunning
	cl.StatusMessage = ""
	cl.Save(paths.Clusters)
	log.Printf("cluster %s: provisioning complete, cluster is running", clusterName)
}

func (s *Server) handleClusterDelete(w http.ResponseWriter, r *http.Request) {
	s.deleteCluster(w, r)
}

func (s *Server) handleClusterDeletePost(w http.ResponseWriter, r *http.Request) {
	s.deleteCluster(w, r)
}

func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.ClusterName(name); err != nil {
		httpError(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	cl, err := cluster.Load(paths.Clusters, name)
	if err != nil {
		httpError(w, r, "Cluster not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	// Delete all VMs in the cluster
	for _, vmName := range cl.AllVMs() {
		existingVM, err := vm.Load(paths.VMs, vmName)
		if err != nil {
			continue
		}

		fcClient.UpdateVMState(existingVM)
		if existingVM.State == vm.StateRunning {
			ctx := context.Background()
			fcClient.StopVM(ctx, existingVM.SocketPath)
			if existingVM.PID > 0 && firecracker.IsFirecrackerProcess(existingVM.PID) {
				if proc, err := os.FindProcess(existingVM.PID); err == nil {
					proc.Signal(syscall.SIGKILL)
				}
			}
			time.Sleep(500 * time.Millisecond)
		}

		if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
			netMgr.DeleteTap(existingVM.TapDevice)
		}
		imgMgr.DeleteVMRootfs(vmName, paths.VMs)
		os.Remove(existingVM.SocketPath)
		vm.Delete(paths.VMs, vmName)
	}

	cluster.Delete(paths.Clusters, name)

	if isHTMXRequest(r) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(""))
	} else {
		http.Redirect(w, r, "/clusters", http.StatusSeeOther)
	}
}

// JSON API handlers

func (s *Server) handleAPIClusterList(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	clusters, err := cluster.List(paths.Clusters)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, clusters)
}

func (s *Server) handleAPIClusterCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string `json:"name"`
		Workers          int    `json:"workers"`
		CPUs             int    `json:"cpus"`
		MemoryMB         int    `json:"memory_mb"`
		DiskSizeMB       int    `json:"disk_size_mb"`
		K8sVersion       string `json:"k8s_version"`
		SSHKey           string `json:"ssh_key"`
		SSHKeyPath       string `json:"ssh_key_path"`
		Kernel           string `json:"kernel"`
		Image            string `json:"image"`
		AdminWorkstation bool   `json:"admin_workstation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		jsonError(w, "Name is required", http.StatusBadRequest)
		return
	}
	if err := validate.ClusterName(req.Name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.SSHKey == "" {
		jsonError(w, "SSH key is required", http.StatusBadRequest)
		return
	}
	if req.SSHKeyPath == "" {
		jsonError(w, "SSH key path is required for cluster provisioning", http.StatusBadRequest)
		return
	}
	if req.CPUs == 0 {
		req.CPUs = 2
	}
	if req.MemoryMB == 0 {
		req.MemoryMB = 4096
	}
	if req.DiskSizeMB == 0 {
		req.DiskSizeMB = 10240
	}
	if req.K8sVersion == "" {
		req.K8sVersion = "1.36.0"
	}
	if err := validate.K8sVersion(req.K8sVersion); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
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
	if req.CPUs < 2 {
		jsonError(w, "Kubernetes requires at least 2 CPUs", http.StatusBadRequest)
		return
	}
	if req.MemoryMB < 2048 {
		jsonError(w, "Kubernetes requires at least 2048 MB memory", http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	s.cfg.EnsureDirectories()

	if cluster.Exists(paths.Clusters, req.Name) {
		jsonError(w, fmt.Sprintf("Cluster '%s' already exists", req.Name), http.StatusConflict)
		return
	}

	cl := cluster.NewCluster(req.Name, req.Workers, req.K8sVersion)
	cl.CPUs = req.CPUs
	cl.MemoryMB = req.MemoryMB
	cl.DiskSizeMB = req.DiskSizeMB
	cl.Image = req.Image
	cl.Kernel = req.Kernel
	cl.SSHKeyPath = req.SSHKeyPath

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	if req.Kernel == "" && imgMgr.KernelExists("k8s-kernel") {
		cl.Kernel = "k8s-kernel"
	}
	if req.Image == "" {
		if found := imgMgr.FindK8sRootfs(req.K8sVersion); found != "" {
			cl.Image = found
		}
	}

	if req.AdminWorkstation {
		secImage := imgMgr.FindSecurityRootfs()
		if secImage == "" {
			jsonError(w, "Admin workstation requires a security-* rootfs image (none found)", http.StatusBadRequest)
			return
		}
		cl.AdminVM = fmt.Sprintf("%s-admin", req.Name)
	}

	if err := cl.Save(paths.Clusters); err != nil {
		jsonError(w, "Failed to save cluster: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, vmName := range cl.AllVMs() {
		newVM := vm.NewVM(vmName)
		if vmName == cl.AdminVM {
			secImage := imgMgr.FindSecurityRootfs()
			newVM.CPUs = 2
			newVM.MemoryMB = 4096
			newVM.DiskSizeMB = 20480
			newVM.Image = secImage
			newVM.Kernel = ""
		} else {
			newVM.CPUs = cl.CPUs
			newVM.MemoryMB = cl.MemoryMB
			newVM.DiskSizeMB = cl.DiskSizeMB
			newVM.Image = cl.Image
			newVM.Kernel = cl.Kernel
		}
		newVM.MacAddress = newVM.GenerateMacAddress()
		newVM.TapDevice = network.GenerateTapName(newVM.ID)
		newVM.SSHPublicKey = req.SSHKey
		newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, vmName)

		if err := newVM.Save(paths.VMs); err != nil {
			cl.State = cluster.StateError
			cl.Save(paths.Clusters)
			jsonError(w, fmt.Sprintf("Failed to create VM '%s': %s", vmName, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	go s.provisionClusterInBackground(req.Name)

	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, cl)
}

func (s *Server) handleAPIClusterDelete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.ClusterName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	paths := s.cfg.GetPaths()

	cl, err := cluster.Load(paths.Clusters, name)
	if err != nil {
		jsonError(w, "Cluster not found", http.StatusNotFound)
		return
	}

	fcClient := firecracker.NewClient()
	netMgr := network.NewManager(s.cfg.BridgeName, s.cfg.Subnet, s.cfg.Gateway, s.cfg.HostInterface)
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	for _, vmName := range cl.AllVMs() {
		existingVM, err := vm.Load(paths.VMs, vmName)
		if err != nil {
			continue
		}

		fcClient.UpdateVMState(existingVM)
		if existingVM.State == vm.StateRunning {
			ctx := context.Background()
			fcClient.StopVM(ctx, existingVM.SocketPath)
			if existingVM.PID > 0 && firecracker.IsFirecrackerProcess(existingVM.PID) {
				if proc, err := os.FindProcess(existingVM.PID); err == nil {
					proc.Signal(syscall.SIGKILL)
				}
			}
			time.Sleep(500 * time.Millisecond)
		}

		if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
			netMgr.DeleteTap(existingVM.TapDevice)
		}
		imgMgr.DeleteVMRootfs(vmName, paths.VMs)
		os.Remove(existingVM.SocketPath)
		vm.Delete(paths.VMs, vmName)
	}

	cluster.Delete(paths.Clusters, name)
	jsonResponse(w, map[string]string{"status": "deleted"})
}
