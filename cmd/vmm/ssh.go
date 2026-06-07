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
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			// Update state
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)

			if existingVM.State != vm.StateRunning {
				return fmt.Errorf("VM '%s' is not running", name)
			}

			if existingVM.IPAddress == "" {
				return fmt.Errorf("VM '%s' has no IP address assigned", name)
			}

			// Build SSH command
			sshArgs := []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
			}

			// Use vmm managed key as primary identity if readable
			vmmKeyPath := sshkey.PrivateKeyPath(paths.SSH)
			if f, err := os.Open(vmmKeyPath); err == nil {
				f.Close()
				sshArgs = append(sshArgs, "-i", vmmKeyPath)
			} else {
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
						sshArgs = append(sshArgs, "-i", keyPath)
						break
					}
				}
			}

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
