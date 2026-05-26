package web

import (
	"log"
	"net/http"

	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/validate"
)

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	kernels, err := imgMgr.ListKernelsWithInfo()
	if err != nil {
		kernels = []image.KernelInfo{}
	}

	rootfs, err := imgMgr.ListRootfsWithInfo()
	if err != nil {
		rootfs = []image.RootfsInfo{}
	}

	available, err := imgMgr.ListAvailableReleases()
	if err != nil {
		log.Printf("Failed to fetch GitHub releases: %v", err)
		available = []image.AvailableRelease{}
	}

	var availableKernels, availableRootfs []image.AvailableRelease
	for _, rel := range available {
		if rel.Type == "kernel" {
			availableKernels = append(availableKernels, rel)
		} else {
			availableRootfs = append(availableRootfs, rel)
		}
	}

	s.renderPage(w, r, "images.html", "images", map[string]interface{}{
		"Kernels":          kernels,
		"Rootfs":           rootfs,
		"AvailableKernels": availableKernels,
		"AvailableRootfs":  availableRootfs,
		"GithubError":      err != nil,
	})
}

func (s *Server) handleKernelDelete(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "missing kernel name", http.StatusBadRequest)
		return
	}
	if err := validate.KernelName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	if err := imgMgr.DeleteKernel(name); err != nil {
		s.renderImagesFlash(w, r, "Failed to delete kernel: "+err.Error(), "error")
		return
	}

	s.renderImagesFlash(w, r, "Kernel '"+name+"' deleted", "success")
}

func (s *Server) handleRootfsDelete(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "missing rootfs name", http.StatusBadRequest)
		return
	}
	if err := validate.ImageName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	if err := imgMgr.DeleteImage(name); err != nil {
		s.renderImagesFlash(w, r, "Failed to delete rootfs: "+err.Error(), "error")
		return
	}

	s.renderImagesFlash(w, r, "Rootfs '"+name+"' deleted", "success")
}

func (s *Server) handleKernelDownload(w http.ResponseWriter, r *http.Request) {
	tag := r.FormValue("tag")
	localName := r.FormValue("local_name")
	if tag == "" || localName == "" {
		http.Error(w, "missing download parameters", http.StatusBadRequest)
		return
	}
	if err := validate.KernelName(localName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	url, err := imgMgr.FindReleaseURL(tag, "kernel")
	if err != nil {
		s.renderImagesFlash(w, r, "Failed to resolve download URL: "+err.Error(), "error")
		return
	}

	if err := imgMgr.DownloadKernelFromRelease(url, localName); err != nil {
		s.renderImagesFlash(w, r, "Failed to download kernel: "+err.Error(), "error")
		return
	}

	s.renderImagesFlash(w, r, "Kernel '"+tag+"' downloaded as '"+localName+"'", "success")
}

func (s *Server) handleRootfsDownload(w http.ResponseWriter, r *http.Request) {
	tag := r.FormValue("tag")
	localName := r.FormValue("local_name")
	if tag == "" || localName == "" {
		http.Error(w, "missing download parameters", http.StatusBadRequest)
		return
	}
	if err := validate.ImageName(localName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	url, err := imgMgr.FindReleaseURL(tag, "rootfs")
	if err != nil {
		s.renderImagesFlash(w, r, "Failed to resolve download URL: "+err.Error(), "error")
		return
	}

	if err := imgMgr.DownloadRootfsFromRelease(url, localName); err != nil {
		s.renderImagesFlash(w, r, "Failed to download rootfs: "+err.Error(), "error")
		return
	}

	s.renderImagesFlash(w, r, "Rootfs '"+tag+"' downloaded as '"+localName+"'", "success")
}

func (s *Server) renderImagesFlash(w http.ResponseWriter, r *http.Request, msg, flashType string) {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	kernels, _ := imgMgr.ListKernelsWithInfo()
	rootfs, _ := imgMgr.ListRootfsWithInfo()

	available, err := imgMgr.ListAvailableReleases()
	var availableKernels, availableRootfs []image.AvailableRelease
	for _, rel := range available {
		if rel.Type == "kernel" {
			availableKernels = append(availableKernels, rel)
		} else {
			availableRootfs = append(availableRootfs, rel)
		}
	}

	s.renderPage(w, r, "images.html", "images", map[string]interface{}{
		"Kernels":          kernels,
		"Rootfs":           rootfs,
		"AvailableKernels": availableKernels,
		"AvailableRootfs":  availableRootfs,
		"GithubError":      err != nil,
		"Flash":            msg,
		"FlashType":        flashType,
	})
}

// JSON API handlers

func (s *Server) handleAPIImageList(w http.ResponseWriter, r *http.Request) {
	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	kernels, _ := imgMgr.ListKernelsWithInfo()
	rootfs, _ := imgMgr.ListRootfsWithInfo()
	available, _ := imgMgr.ListAvailableReleases()

	jsonResponse(w, map[string]interface{}{
		"kernels":   kernels,
		"rootfs":    rootfs,
		"available": available,
	})
}

func (s *Server) handleAPIKernelDelete(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		jsonError(w, "missing kernel name", http.StatusBadRequest)
		return
	}
	if err := validate.KernelName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	if err := imgMgr.DeleteKernel(name); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleAPIRootfsDelete(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		jsonError(w, "missing rootfs name", http.StatusBadRequest)
		return
	}
	if err := validate.ImageName(name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	paths := s.cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

	if err := imgMgr.DeleteImage(name); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": "deleted"})
}
