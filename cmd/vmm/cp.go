package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/spf13/cobra"
)

func cpCmd() *cobra.Command {
	var user string
	var recursive bool

	cmd := &cobra.Command{
		Use:   "cp <src> <dest>",
		Short: "Copy files between the host and a running microVM",
		Long: `Copy files or directories between the host and a running microVM over SCP.

Exactly one of <src> or <dest> must refer to the VM, written as <vm>:<path>.
A relative guest path is relative to the user's home directory, and an empty
guest path (<vm>:) means the home directory itself. To copy a host path that
contains a colon, prefix it with ./ or use an absolute path.

Examples:
  vmm cp ./app.tar.gz myvm:/tmp/
  vmm cp myvm:/var/log/syslog ./syslog
  vmm cp -r ./src myvm:/root/src
  vmm cp -u ubuntu ./notes.txt myvm:`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeCopyArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := parseCopyArgs(args[0], args[1])
			if err != nil {
				return err
			}

			existingVM, err := loadRunningVM(spec.vmName)
			if err != nil {
				return err
			}

			guest := fmt.Sprintf("%s@%s:%s", user, existingVM.IPAddress, spec.guestPath)
			src, dest := spec.hostPath, guest
			if !spec.toVM {
				src, dest = guest, spec.hostPath
			}

			scpArgs := append(sshHostKeyArgs(), "-o", "LogLevel=ERROR")
			scpArgs = append(scpArgs, sshIdentityArgs()...)
			if recursive {
				scpArgs = append(scpArgs, "-r")
			}
			// "--" stops a host path beginning with "-" being read as an option
			scpArgs = append(scpArgs, "--", src, dest)

			scpExec := exec.Command("scp", scpArgs...)
			scpExec.Stdin = os.Stdin
			scpExec.Stdout = os.Stdout
			scpExec.Stderr = os.Stderr

			if err := scpExec.Run(); err != nil {
				return fmt.Errorf("copy failed: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&user, "user", "u", "root", "SSH user in the VM")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Copy directories recursively")

	return cmd
}

// copySpec is a parsed vmm cp invocation: one host path, one guest path and
// the direction of the copy.
type copySpec struct {
	vmName    string
	guestPath string
	hostPath  string
	toVM      bool
}

// parseCopyArgs works out which side of a copy is the VM.
func parseCopyArgs(src, dest string) (copySpec, error) {
	srcVM, srcPath, srcRemote := splitVMPath(src)
	destVM, destPath, destRemote := splitVMPath(dest)

	var spec copySpec
	switch {
	case srcRemote && destRemote:
		return spec, fmt.Errorf("copying directly between VMs is not supported; copy via the host instead")
	case !srcRemote && !destRemote:
		return spec, fmt.Errorf("one of <src> or <dest> must be a VM path in the form <vm>:<path>")
	case srcRemote:
		spec = copySpec{vmName: srcVM, guestPath: srcPath, hostPath: dest}
	default:
		spec = copySpec{vmName: destVM, guestPath: destPath, hostPath: src, toVM: true}
	}

	if err := validate.VMName(spec.vmName); err != nil {
		return copySpec{}, err
	}
	if spec.hostPath == "" {
		return copySpec{}, fmt.Errorf("host path must not be empty")
	}
	return spec, nil
}

// splitVMPath reports whether arg is a VM path (<vm>:<path>), returning the
// VM name and guest path if so. Anything with a "/" before the first colon is
// a host path, so ./a:b and /tmp/a:b stay local.
func splitVMPath(arg string) (string, string, bool) {
	name, path, found := strings.Cut(arg, ":")
	if !found || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return name, path, true
}

// completeCopyArgs offers "<vm>:" for each VM alongside normal file completion.
func completeCopyArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) >= 2 || strings.Contains(toComplete, ":") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, _ := completeVMNames(cmd, nil, toComplete)
	for i, name := range names {
		names[i] = name + ":"
	}
	return names, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveDefault
}
