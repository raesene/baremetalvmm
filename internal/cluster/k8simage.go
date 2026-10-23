package cluster

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/image"
)

// DefaultK8sMinor is the Kubernetes minor version clusters get when no
// version or image is given: its newest patch with a pre-built rootfs.
const DefaultK8sMinor = "1.37"

// ChooseK8sImage picks a kubeadm cluster's rootfs image and Kubernetes
// version, so the two can't disagree. A k8s-<version> image supplies its
// own version (an explicit, different version is an error). Without an
// image, version (exact, or a minor for its newest patch; empty for
// DefaultK8sMinor) selects one, downloading it if needed. An exact version
// with no pre-built image returns no image, and provisioning installs that
// version. A custom image keeps version as given; if empty, provisioning
// takes it from the image's kubeadm.
func ChooseK8sImage(imgMgr *image.Manager, imageName, version string) (string, string, error) {
	if imageName != "" {
		v := image.K8sImageVersion(imageName)
		if v == "" {
			return imageName, version, nil
		}
		if version != "" && version != v {
			return "", "", fmt.Errorf("image '%s' has Kubernetes %s, but version %s was requested", imageName, v, version)
		}
		return imageName, v, nil
	}
	if version == "" {
		version = DefaultK8sMinor
	}
	return imgMgr.ResolveK8sImage(version)
}
