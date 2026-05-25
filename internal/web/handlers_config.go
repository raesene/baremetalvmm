package web

import (
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/image"
)

func (s *Server) configPageData() map[string]interface{} {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	kernels, _ := imgMgr.ListKernelsWithInfo()
	images, _ := imgMgr.ListRootfsWithInfo()

	return map[string]interface{}{
		"Config":  s.cfg,
		"Kernels": kernels,
		"Images":  images,
	}
}

func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "config.html", "config", s.configPageData())
}

func (s *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderConfigFlash(w, r, "Failed to parse form: "+err.Error(), "error")
		return
	}

	defaults := &config.VMDefaults{}

	if v := strings.TrimSpace(r.FormValue("default_cpus")); v != "" && v != "0" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.renderConfigFlash(w, r, "Invalid default vCPUs value", "error")
			return
		}
		defaults.CPUs = n
	}

	if v := strings.TrimSpace(r.FormValue("default_memory")); v != "" && v != "0" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 128 {
			s.renderConfigFlash(w, r, "Invalid default memory value (minimum 128 MB)", "error")
			return
		}
		defaults.MemoryMB = n
	}

	if v := strings.TrimSpace(r.FormValue("default_disk")); v != "" && v != "0" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 256 {
			s.renderConfigFlash(w, r, "Invalid default disk value (minimum 256 MB)", "error")
			return
		}
		defaults.DiskSizeMB = n
	}

	defaults.Image = strings.TrimSpace(r.FormValue("default_image"))
	defaults.Kernel = strings.TrimSpace(r.FormValue("default_kernel"))
	defaults.SSHKeyPath = strings.TrimSpace(r.FormValue("default_ssh_key"))

	if dns := strings.TrimSpace(r.FormValue("default_dns")); dns != "" {
		for _, d := range strings.Split(dns, ",") {
			if trimmed := strings.TrimSpace(d); trimmed != "" {
				defaults.DNSServers = append(defaults.DNSServers, trimmed)
			}
		}
	}

	s.cfg.DataDir = strings.TrimSpace(r.FormValue("data_dir"))
	s.cfg.BridgeName = strings.TrimSpace(r.FormValue("bridge_name"))
	s.cfg.Subnet = strings.TrimSpace(r.FormValue("subnet"))
	s.cfg.Gateway = strings.TrimSpace(r.FormValue("gateway"))
	s.cfg.HostInterface = strings.TrimSpace(r.FormValue("host_interface"))
	s.cfg.KernelPath = strings.TrimSpace(r.FormValue("kernel_path"))
	s.cfg.RootfsPath = strings.TrimSpace(r.FormValue("rootfs_path"))

	hasDefaults := defaults.CPUs != 0 || defaults.MemoryMB != 0 || defaults.DiskSizeMB != 0 ||
		defaults.Image != "" || defaults.Kernel != "" || defaults.SSHKeyPath != "" || len(defaults.DNSServers) > 0
	if hasDefaults {
		s.cfg.VMDefaults = defaults
	} else {
		s.cfg.VMDefaults = nil
	}

	if err := s.cfg.Save(s.configPath); err != nil {
		s.renderConfigFlash(w, r, "Failed to save config: "+err.Error(), "error")
		return
	}

	s.renderConfigFlash(w, r, "Configuration saved", "success")
}

func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	service := r.FormValue("service")

	var unitName string
	switch service {
	case "vmm":
		unitName = "vmm.service"
	case "vmm-web":
		unitName = "vmm-web.service"
	default:
		s.renderConfigFlash(w, r, "Unknown service: "+service, "error")
		return
	}

	cmd := exec.Command("systemctl", "restart", unitName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to restart %s: %v, output: %s", unitName, err, output)
		s.renderConfigFlash(w, r, fmt.Sprintf("Failed to restart %s: %v", unitName, err), "error")
		return
	}

	s.renderConfigFlash(w, r, fmt.Sprintf("Service %s restarted", unitName), "success")
}

func (s *Server) renderConfigFlash(w http.ResponseWriter, r *http.Request, msg, flashType string) {
	data := s.configPageData()
	data["Flash"] = msg
	data["FlashType"] = flashType
	s.renderPage(w, r, "config.html", "config", data)
}
