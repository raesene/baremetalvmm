package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func sshCmd() *cobra.Command {
	var user string

	cmd := &cobra.Command{
		Use:               "ssh <name> [-- <ssh-args>]",
		Short:             "SSH into a microVM",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			existingVM, err := loadRunningVM(args[0])
			if err != nil {
				return err
			}

			// Build SSH command
			sshArgs := append(sshHostKeyArgs(), sshIdentityArgs()...)
			sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, existingVM.IPAddress))

			// Append any additional SSH args
			if len(args) > 1 {
				sshArgs = append(sshArgs, args[1:]...)
			}

			// Execute SSH
			sshExec := exec.Command("ssh", sshArgs...)
			sshExec.Stdin = os.Stdin
			sshExec.Stdout = os.Stdout
			sshExec.Stderr = os.Stderr

			return sshExec.Run()
		},
	}

	cmd.Flags().StringVarP(&user, "user", "u", "root", "SSH user")

	return cmd
}

// loadRunningVM loads a VM by name and checks that it is running with an IP
// address, which is required before vmm can reach it over SSH.
func loadRunningVM(name string) (*vm.VM, error) {
	if err := validate.VMName(name); err != nil {
		return nil, err
	}
	paths := cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, name)
	if err != nil {
		return nil, fmt.Errorf("VM '%s' not found", name)
	}

	// Update state
	fcClient := firecracker.NewClient()
	fcClient.UpdateVMState(existingVM)

	if existingVM.State != vm.StateRunning {
		return nil, fmt.Errorf("VM '%s' is not running", name)
	}

	if existingVM.IPAddress == "" {
		return nil, fmt.Errorf("VM '%s' has no IP address assigned", name)
	}

	return existingVM, nil
}

// sshHostKeyArgs disables host key checking: VM host keys change whenever a
// VM is recreated with a reused IP, so pinning them only produces noise.
func sshHostKeyArgs() []string {
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
	}
}

// sshIdentityArgs returns the -i argument for ssh/scp, preferring the vmm
// managed key and falling back to the invoking user's own keys.
func sshIdentityArgs() []string {
	// Use vmm managed key as primary identity if readable
	vmmKeyPath := sshkey.PrivateKeyPath(cfg.GetPaths().SSH)
	if f, err := os.Open(vmmKeyPath); err == nil {
		f.Close()
		return []string{"-i", vmmKeyPath}
	}

	// Fall back to user's SSH keys when managed key isn't readable
	var userHome string
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
		userHome = fmt.Sprintf("/home/%s", sudoUser)
	} else {
		userHome, _ = os.UserHomeDir()
	}
	for _, keyFile := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
		keyPath := fmt.Sprintf("%s/.ssh/%s", userHome, keyFile)
		if _, statErr := os.Stat(keyPath); statErr == nil {
			return []string{"-i", keyPath}
		}
	}
	return nil
}
