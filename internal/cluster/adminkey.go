package cluster

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Locations on the admin workstation for the cluster admin key and the
// matching ssh client config (picked up by Ubuntu's default ssh_config).
const (
	adminKeyRemotePath    = "/root/.ssh/vmm_cluster_ed25519"
	adminSSHConfigPath    = "/etc/ssh/ssh_config.d/vmm-cluster.conf"
	adminKeyHeredocMarker = "VMMADMINKEYEOF"
)

// AdminSSHConfig renders an ssh client config that lets root on the admin
// workstation reach each cluster node by VM name or IP with the cluster key.
func AdminSSHConfig(clusterName string, nodes []NodeInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by vmm: SSH access to the nodes of cluster %s\n", clusterName)
	for _, n := range nodes {
		fmt.Fprintf(&b, "\nHost %s %s\n", n.Name, n.IP)
		fmt.Fprintf(&b, "    HostName %s\n", n.IP)
		b.WriteString("    User root\n")
		fmt.Fprintf(&b, "    IdentityFile %s\n", adminKeyRemotePath)
		b.WriteString("    IdentitiesOnly yes\n")
		b.WriteString("    StrictHostKeyChecking accept-new\n")
	}
	return b.String()
}

// InstallAdminKey copies the cluster admin private key to the admin workstation
// and configures ssh there so `ssh <node>` works for every cluster node. The
// nodes already trust the matching public key via their authorized_keys.
func InstallAdminKey(adminIP string, sshKeys []string, clusterKeyPath, clusterName string, nodes []NodeInfo) error {
	privKey, err := os.ReadFile(clusterKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read cluster admin key: %w", err)
	}

	client, err := WaitForSSH(adminIP, sshKeys, 60*time.Second)
	if err != nil {
		return fmt.Errorf("SSH to admin workstation failed: %w", err)
	}
	defer client.Close()

	commands := []string{
		"mkdir -p /root/.ssh /etc/ssh/ssh_config.d && chmod 700 /root/.ssh",
		fmt.Sprintf("umask 077 && cat > %s << '%s'\n%s\n%s",
			adminKeyRemotePath, adminKeyHeredocMarker, strings.TrimSpace(string(privKey)), adminKeyHeredocMarker),
		"chmod 600 " + adminKeyRemotePath,
		fmt.Sprintf("cat > %s << '%s'\n%s%s",
			adminSSHConfigPath, adminKeyHeredocMarker, AdminSSHConfig(clusterName, nodes), adminKeyHeredocMarker),
		"chmod 644 " + adminSSHConfigPath,
	}
	for _, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("failed to install cluster admin key: %w", err)
		}
	}
	return nil
}
