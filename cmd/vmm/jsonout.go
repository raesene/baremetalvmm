package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

// writeJSON prints v as indented JSON. Lists are printed as [] when empty,
// never as a message, so callers must pass non-nil slices.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// reserveStdoutForJSON points os.Stdout at stderr for the rest of a command,
// so the progress it prints (and the output of anything it runs) can't mix
// with its JSON result. It returns the real stdout for the result.
func reserveStdoutForJSON() (stdout *os.File, restore func()) {
	stdout = os.Stdout
	os.Stdout = os.Stderr
	return stdout, func() { os.Stdout = stdout }
}

// vmJSON is a VM as `vmm list --json` prints it: the stored record, with the
// backend filled in (it is stored empty for Firecracker).
func vmJSON(v *vm.VM) vm.VM {
	out := *v
	out.Backend = hypervisor.Name(v)
	return out
}

func clusterStatuses(clusters []*cluster.Cluster, vmsDir string) []cluster.Status {
	out := make([]cluster.Status, 0, len(clusters))
	for _, cl := range clusters {
		out = append(out, cl.Status(vmsDir))
	}
	return out
}

func clusterShowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:               "show <name>",
		Short:             "Show a cluster's nodes, addresses and state",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClusterNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.ClusterName(name); err != nil {
				return err
			}
			paths := cfg.GetPaths()
			cl, err := cluster.Load(paths.Clusters, name)
			if err != nil {
				return fmt.Errorf("cluster '%s' not found", name)
			}
			st := cl.Status(paths.VMs)
			if jsonOutput {
				return writeJSON(os.Stdout, st)
			}
			printClusterStatus(os.Stdout, st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the cluster as JSON")
	return cmd
}

func printClusterStatus(out io.Writer, st cluster.Status) {
	version := st.K8sVersion
	if st.Type == cluster.DistroOpenShift {
		version = st.OpenShiftVersion
	}
	fmt.Fprintf(out, "Cluster:        %s\n", st.Name)
	fmt.Fprintf(out, "State:          %s\n", st.State)
	if st.StatusMessage != "" {
		fmt.Fprintf(out, "Message:        %s\n", st.StatusMessage)
	}
	fmt.Fprintf(out, "Type:           %s %s\n", st.Type, version)
	fmt.Fprintf(out, "CNI:            %s\n", st.CNI)
	if st.Nested {
		fmt.Fprintln(out, "Node backend:   cloud-hypervisor (nested)")
	}
	fmt.Fprintf(out, "Control plane:  %s\n", orDash(st.ControlPlaneIP))
	fmt.Fprintf(out, "Context:        %s\n\n", st.Context)

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ROLE\tVM\tSTATE\tIP ADDRESS")
	for _, n := range st.Nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n.Role, n.Name, n.State, orDash(n.IP))
	}
	if a := st.AdminWorkstation; a != nil {
		fmt.Fprintf(w, "admin\t%s\t%s\t%s\n", a.Name, a.State, orDash(a.IP))
	}
	w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
