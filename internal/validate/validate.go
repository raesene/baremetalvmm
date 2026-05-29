package validate

import (
	"fmt"
	"net"
	"regexp"
)

var identifierRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func Name(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s name cannot be empty", kind)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("%s name cannot be '.' or '..'", kind)
	}
	if !identifierRe.MatchString(value) {
		return fmt.Errorf("%s name %q is invalid: must be 1-64 characters, start with a letter or digit, and contain only letters, digits, dots, hyphens, or underscores", kind, value)
	}
	return nil
}

func VMName(name string) error {
	return Name("VM", name)
}

func ClusterName(name string) error {
	return Name("cluster", name)
}

func ImageName(name string) error {
	return Name("image", name)
}

func KernelName(name string) error {
	return Name("kernel", name)
}

func MountTag(tag string) error {
	return Name("mount tag", tag)
}

func CPUs(n int) error {
	if n < 1 || n > 32 {
		return fmt.Errorf("CPUs must be between 1 and 32 (got %d)", n)
	}
	return nil
}

func MemoryMB(n int) error {
	if n < 128 || n > 65536 {
		return fmt.Errorf("memory must be between 128 and 65536 MB (got %d)", n)
	}
	return nil
}

func DiskSizeMB(n int) error {
	if n < 256 || n > 1048576 {
		return fmt.Errorf("disk size must be between 256 and 1048576 MB (got %d)", n)
	}
	return nil
}

func DNSServer(addr string) error {
	if net.ParseIP(addr) == nil {
		return fmt.Errorf("invalid DNS server address: %q", addr)
	}
	return nil
}

var k8sVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]{1,2}\.[0-9]{1,3}$`)

func K8sVersion(version string) error {
	if !k8sVersionRe.MatchString(version) {
		return fmt.Errorf("invalid Kubernetes version %q: must be in format MAJOR.MINOR.PATCH (e.g., 1.36.0)", version)
	}
	return nil
}

var openShiftVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]{1,2}$`)

// OpenShiftVersion validates an OpenShift/MicroShift major.minor version (e.g. "4.20").
func OpenShiftVersion(version string) error {
	if !openShiftVersionRe.MatchString(version) {
		return fmt.Errorf("invalid OpenShift version %q: must be in format MAJOR.MINOR (e.g., 4.20)", version)
	}
	return nil
}
