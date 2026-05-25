package validate

import (
	"fmt"
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
