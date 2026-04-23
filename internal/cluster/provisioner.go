package cluster

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type SSHClient struct {
	IP      string
	User    string
	KeyPath string
	client  *ssh.Client
}

func NewSSHClient(ip, keyPath string) *SSHClient {
	return &SSHClient{
		IP:      ip,
		User:    "root",
		KeyPath: keyPath,
	}
}

func (s *SSHClient) Connect() error {
	var authMethods []ssh.AuthMethod

	// Try SSH agent first (handles passphrase-protected keys that are already unlocked)
	if agentSock := os.Getenv("SSH_AUTH_SOCK"); agentSock != "" {
		if conn, err := net.Dial("unix", agentSock); err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	// Fall back to reading the key file directly (works for unencrypted keys)
	keyData, err := os.ReadFile(s.KeyPath)
	if err != nil {
		if len(authMethods) == 0 {
			return fmt.Errorf("failed to read SSH key %s: %w", s.KeyPath, err)
		}
	} else {
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			if len(authMethods) == 0 {
				return fmt.Errorf("failed to parse SSH key (if the key has a passphrase, ensure ssh-agent is running with the key loaded): %w", err)
			}
		} else {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	config := &ssh.ClientConfig{
		User:            s.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(s.IP, "22"), config)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", s.IP, err)
	}
	s.client = client
	return nil
}

func (s *SSHClient) Run(cmd string) (string, error) {
	if s.client == nil {
		return "", fmt.Errorf("not connected")
	}
	session, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()
	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w\nOutput: %s", err, string(output))
	}
	return string(output), nil
}

func (s *SSHClient) Close() {
	if s.client != nil {
		s.client.Close()
	}
}

func WaitForSSH(ip, keyPath string, timeout time.Duration) (*SSHClient, error) {
	deadline := time.Now().Add(timeout)
	client := NewSSHClient(ip, keyPath)
	for time.Now().Before(deadline) {
		if err := client.Connect(); err == nil {
			if _, err := client.Run("echo ready"); err == nil {
				return client, nil
			}
			client.Close()
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("SSH to %s not available after %s", ip, timeout)
}

func installContainerd(client *SSHClient) error {
	commands := []string{
		// Install kmod and containerd
		"DEBIAN_FRONTEND=noninteractive apt-get update -qq ",
		"DEBIAN_FRONTEND=noninteractive apt-get install -y -qq containerd kmod ",
		// Kernel modules (best-effort - may be built into kernel in Firecracker VMs)
		`cat <<'EOF' > /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF`,
		"modprobe overlay 2>/dev/null || true",
		"modprobe br_netfilter 2>/dev/null || true",
		// Sysctl - ip_forward is always available, bridge settings only if br_netfilter loaded
		`cat <<'EOF' > /etc/sysctl.d/k8s.conf
net.ipv4.ip_forward = 1
EOF`,
		// Add bridge sysctl only if br_netfilter is available
		`if [ -d /proc/sys/net/bridge ]; then
echo "net.bridge.bridge-nf-call-iptables = 1" >> /etc/sysctl.d/k8s.conf
echo "net.bridge.bridge-nf-call-ip6tables = 1" >> /etc/sysctl.d/k8s.conf
fi`,
		"sysctl -w net.ipv4.ip_forward=1",
		// Shared mount propagation (required for Kubernetes and Cilium)
		"mount --make-rshared /",
		// Mount bpf filesystem (required by Cilium)
		"mount -t bpf bpf /sys/fs/bpf || true",
		// Configure containerd with SystemdCgroup
		"mkdir -p /etc/containerd",
		"containerd config default > /etc/containerd/config.toml",
		"sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml",
		"systemctl restart containerd",
		"systemctl enable containerd",
	}
	for i, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("containerd install step %d failed: %w\nCommand: %s", i+1, err, cmd)
		}
	}
	return nil
}

func installKubeadm(client *SSHClient, k8sVersion string) error {
	parts := strings.SplitN(k8sVersion, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("invalid k8s version: %s", k8sVersion)
	}
	majorMinor := parts[0] + "." + parts[1]

	commands := []string{
		"DEBIAN_FRONTEND=noninteractive apt-get install -y -qq apt-transport-https ca-certificates curl gpg ",
		"mkdir -p /etc/apt/keyrings",
		fmt.Sprintf(`curl -fsSL "https://pkgs.k8s.io/core:/stable:/v%s/deb/Release.key" | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg 2>&1`, majorMinor),
		fmt.Sprintf(`echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v%s/deb/ /" > /etc/apt/sources.list.d/kubernetes.list`, majorMinor),
	}

	minor := 0
	if len(parts) >= 2 {
		fmt.Sscanf(parts[1], "%d", &minor)
	}
	if minor >= 36 {
		commands = append(commands,
			`curl -fsSL "https://pkgs.k8s.io/core:/stable:/v1.35/deb/Release.key" | gpg --dearmor -o /etc/apt/keyrings/kubernetes-deps-keyring.gpg 2>&1`,
			`echo "deb [signed-by=/etc/apt/keyrings/kubernetes-deps-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.35/deb/ /" > /etc/apt/sources.list.d/kubernetes-deps.list`,
		)
	}

	commands = append(commands,
		"apt-get update -qq ",
		fmt.Sprintf("DEBIAN_FRONTEND=noninteractive apt-get install -y -qq kubelet=%s-* kubeadm=%s-* kubectl=%s-* ", k8sVersion, k8sVersion, k8sVersion),
		"apt-mark hold kubelet kubeadm kubectl ",
		"systemctl enable kubelet",
	)
	for i, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("kubeadm install step %d failed: %w\nCommand: %s", i+1, err, cmd)
		}
	}
	return nil
}

func setHostname(client *SSHClient, hostname, ip string) error {
	cmd := fmt.Sprintf(`hostnamectl set-hostname %s 2>/dev/null; echo "%s %s" >> /etc/hosts`, hostname, ip, hostname)
	_, err := client.Run(cmd)
	return err
}

func initControlPlane(client *SSHClient, cl *Cluster) error {
	if err := setHostname(client, cl.ControlPlaneVM, cl.ControlPlaneIP); err != nil {
		return fmt.Errorf("failed to set hostname: %w", err)
	}

	cmd := fmt.Sprintf(
		"kubeadm init --kubernetes-version=%s --pod-network-cidr=%s --service-cidr=%s --apiserver-advertise-address=%s --node-name=%s --skip-phases=addon/kube-proxy --ignore-preflight-errors=SystemVerification ",
		cl.K8sVersion, cl.PodSubnet, cl.ServiceSubnet, cl.ControlPlaneIP, cl.ControlPlaneVM,
	)
	output, err := client.Run(cmd)
	if err != nil {
		return fmt.Errorf("kubeadm init failed: %w\n%s", err, output)
	}

	// Extract join command
	joinOutput, err := client.Run("kubeadm token create --print-join-command 2>/dev/null")
	if err != nil {
		return fmt.Errorf("failed to get join command: %w", err)
	}

	// Parse: kubeadm join IP:6443 --token TOKEN --discovery-token-ca-cert-hash sha256:HASH
	joinOutput = strings.TrimSpace(joinOutput)
	fields := strings.Fields(joinOutput)
	for i, f := range fields {
		if f == "--token" && i+1 < len(fields) {
			cl.JoinToken = fields[i+1]
		}
		if f == "--discovery-token-ca-cert-hash" && i+1 < len(fields) {
			cl.JoinCAHash = fields[i+1]
		}
	}

	if cl.JoinToken == "" || cl.JoinCAHash == "" {
		return fmt.Errorf("failed to parse join token from output: %s", joinOutput)
	}
	return nil
}

func installCilium(client *SSHClient, controlPlaneIP string) error {
	commands := []string{
		"export KUBECONFIG=/etc/kubernetes/admin.conf && " +
			`CILIUM_CLI_VERSION=$(curl -s https://raw.githubusercontent.com/cilium/cilium-cli/main/stable.txt) && ` +
			`curl -L --fail --remote-name-all "https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/cilium-linux-amd64.tar.gz" 2>/dev/null && ` +
			`tar xzf cilium-linux-amd64.tar.gz -C /usr/local/bin && ` +
			`rm -f cilium-linux-amd64.tar.gz`,
		fmt.Sprintf("export KUBECONFIG=/etc/kubernetes/admin.conf && cilium install --set kubeProxyReplacement=true --set k8sServiceHost=%s --set k8sServicePort=6443 ", controlPlaneIP),
	}
	for _, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("cilium install failed: %w", err)
		}
	}
	return nil
}

func joinWorker(client *SSHClient, workerIP, controlPlaneIP, token, caHash, nodeName string) error {
	if err := setHostname(client, nodeName, workerIP); err != nil {
		return fmt.Errorf("failed to set hostname: %w", err)
	}
	cmd := fmt.Sprintf(
		"kubeadm join %s:6443 --token %s --discovery-token-ca-cert-hash %s --node-name %s --ignore-preflight-errors=SystemVerification ",
		controlPlaneIP, token, caHash, nodeName,
	)
	if _, err := client.Run(cmd); err != nil {
		return fmt.Errorf("worker join failed: %w", err)
	}
	return nil
}

func waitForNodesReady(client *SSHClient, expectedNodes int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := client.Run("export KUBECONFIG=/etc/kubernetes/admin.conf && kubectl get nodes --no-headers 2>/dev/null")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(output), "\n")
			readyCount := 0
			for _, line := range lines {
				if strings.Contains(line, " Ready") && !strings.Contains(line, "NotReady") {
					readyCount++
				}
			}
			if readyCount >= expectedNodes {
				return nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %d nodes to be Ready after %s", expectedNodes, timeout)
}

type NodeInfo struct {
	Name string
	IP   string
}

func checkKubeadmPreinstalled(client *SSHClient) bool {
	_, err := client.Run("which kubeadm")
	return err == nil
}

func preparePreinstalledNode(client *SSHClient) error {
	commands := []string{
		"modprobe overlay 2>/dev/null || true",
		"modprobe br_netfilter 2>/dev/null || true",
		"sysctl -w net.ipv4.ip_forward=1",
		`if [ -d /proc/sys/net/bridge ]; then
sysctl -w net.bridge.bridge-nf-call-iptables=1 2>/dev/null || true
sysctl -w net.bridge.bridge-nf-call-ip6tables=1 2>/dev/null || true
fi`,
		"mount --make-rshared /",
		"mount -t bpf bpf /sys/fs/bpf || true",
		"systemctl restart containerd",
	}
	for _, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("prepare step failed: %w\nCommand: %s", err, cmd)
		}
	}
	return nil
}

func ProvisionCluster(cl *Cluster, sshKeyPath string, nodes []NodeInfo) error {
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes to provision")
	}

	cpNode := nodes[0]
	workerNodes := nodes[1:]
	cl.ControlPlaneIP = cpNode.IP

	// Wait for SSH on all nodes
	fmt.Println("Waiting for VMs to be SSH-accessible...")
	clients := make(map[string]*SSHClient)
	for _, node := range nodes {
		fmt.Printf("  Waiting for %s (%s)...\n", node.Name, node.IP)
		client, err := WaitForSSH(node.IP, sshKeyPath, 120*time.Second)
		if err != nil {
			return fmt.Errorf("SSH to %s failed: %w", node.Name, err)
		}
		clients[node.Name] = client
		defer client.Close()
	}
	fmt.Println("All VMs are SSH-accessible.")

	// Check if kubeadm is pre-installed (k8s rootfs image)
	preinstalled := checkKubeadmPreinstalled(clients[cpNode.Name])

	if preinstalled {
		fmt.Println("Kubernetes components detected in rootfs image, skipping installation.")
		fmt.Println("Preparing nodes...")
		if err := runOnAllNodes(clients, nodes, func(c *SSHClient, name string) error {
			fmt.Printf("  Preparing %s...\n", name)
			return preparePreinstalledNode(c)
		}); err != nil {
			return err
		}
		fmt.Println("All nodes prepared.")
	} else {
		// Install containerd on all nodes in parallel
		fmt.Println("Installing containerd on all nodes...")
		if err := runOnAllNodes(clients, nodes, func(c *SSHClient, name string) error {
			fmt.Printf("  Installing containerd on %s...\n", name)
			return installContainerd(c)
		}); err != nil {
			return err
		}
		fmt.Println("Containerd installed on all nodes.")

		// Install kubeadm on all nodes in parallel
		fmt.Printf("Installing kubeadm %s on all nodes...\n", cl.K8sVersion)
		if err := runOnAllNodes(clients, nodes, func(c *SSHClient, name string) error {
			fmt.Printf("  Installing kubeadm on %s...\n", name)
			return installKubeadm(c, cl.K8sVersion)
		}); err != nil {
			return err
		}
		fmt.Println("Kubeadm installed on all nodes.")
	}

	// Initialize control plane
	fmt.Printf("Initializing control plane on %s...\n", cpNode.Name)
	cpClient := clients[cpNode.Name]
	if err := initControlPlane(cpClient, cl); err != nil {
		return err
	}
	fmt.Println("Control plane initialized.")

	// Install Cilium
	fmt.Println("Installing Cilium CNI...")
	if err := installCilium(cpClient, cl.ControlPlaneIP); err != nil {
		return err
	}
	fmt.Println("Cilium installed.")

	// Join workers
	if len(workerNodes) > 0 {
		fmt.Println("Joining worker nodes...")
		var wg sync.WaitGroup
		errCh := make(chan error, len(workerNodes))
		for _, worker := range workerNodes {
			wg.Add(1)
			go func(w NodeInfo) {
				defer wg.Done()
				fmt.Printf("  Joining %s...\n", w.Name)
				if err := joinWorker(clients[w.Name], w.IP, cl.ControlPlaneIP, cl.JoinToken, cl.JoinCAHash, w.Name); err != nil {
					errCh <- fmt.Errorf("%s: %w", w.Name, err)
				}
			}(worker)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			return err
		}
		fmt.Println("All workers joined.")
	}

	// Wait for all nodes to be Ready
	expectedNodes := 1 + len(workerNodes)
	fmt.Printf("Waiting for %d node(s) to be Ready...\n", expectedNodes)
	if err := waitForNodesReady(cpClient, expectedNodes, 5*time.Minute); err != nil {
		return err
	}
	fmt.Println("All nodes are Ready.")

	return nil
}

func runOnAllNodes(clients map[string]*SSHClient, nodes []NodeInfo, fn func(*SSHClient, string) error) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(nodes))
	for _, node := range nodes {
		wg.Add(1)
		go func(n NodeInfo) {
			defer wg.Done()
			if err := fn(clients[n.Name], n.Name); err != nil {
				errCh <- fmt.Errorf("%s: %w", n.Name, err)
			}
		}(node)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}
