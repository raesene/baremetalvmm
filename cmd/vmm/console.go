package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func consoleCmd() *cobra.Command {
	var fullLog bool
	var follow bool
	var lines int

	cmd := &cobra.Command{
		Use:               "console <name>",
		Short:             "View serial console output of a microVM",
		Long:              "Display captured serial console output from a VM, including kernel boot messages and crash output.",
		Args:              cobra.ExactArgs(1),
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

			logPath := fmt.Sprintf("%s/%s.log", paths.Logs, existingVM.Name)
			consolePath := firecracker.ConsoleLogPath(logPath)

			if _, err := os.Stat(consolePath); err != nil {
				return fmt.Errorf("no console log found for VM '%s' (has it been started?)", name)
			}

			if fullLog {
				f, err := os.Open(consolePath)
				if err != nil {
					return fmt.Errorf("failed to open console log: %w", err)
				}
				defer f.Close()
				if _, err := io.Copy(os.Stdout, f); err != nil {
					return fmt.Errorf("failed to read console log: %w", err)
				}
				return nil
			}

			return tailFollow(consolePath, lines, follow)
		},
	}

	cmd.Flags().BoolVar(&fullLog, "full", false, "Show the complete console log")
	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "Follow new output (use --follow=false to just show tail)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show from the end")

	return cmd
}

// tailFollow shows the last n lines of a file and optionally follows new output.
func tailFollow(path string, n int, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open console log: %w", err)
	}
	defer f.Close()

	// Read all lines to get the tail
	var allLines []string
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read console log: %w", err)
	}

	// Print the last n lines
	start := 0
	if len(allLines) > n {
		start = len(allLines) - n
	}
	for _, line := range allLines[start:] {
		fmt.Println(line)
	}

	if !follow {
		return nil
	}

	// Follow: keep reading from current position
	offset, _ := f.Seek(0, io.SeekCurrent)
	for {
		f.Seek(offset, io.SeekStart)
		scanner = bufio.NewScanner(f)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			fmt.Println(scanner.Text())
		}
		offset, _ = f.Seek(0, io.SeekCurrent)
		time.Sleep(200 * time.Millisecond)
	}
}
