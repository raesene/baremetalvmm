package cluster

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/validate"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type SSHClient struct {
	IP   string
	User string
	// KeyPaths are private keys to offer, in order, after any ssh-agent keys.
	// Use SSHKeys to build the list, so vmm's managed key is always the last
	// resort.
	KeyPaths []string
	client   *ssh.Client
	port     string // "" means 22; set by tests
}

func NewSSHClient(ip string, keyPaths []string) *SSHClient {
	return &SSHClient{
		IP:       ip,
		User:     "root",
		KeyPaths: keyPaths,
	}
}

// SSHKeys returns the private keys to try for SSH to a cluster's VMs: the
// user's key, if there is one, then vmm's managed key, which
// sshkey.BuildAuthorizedKeys puts on every VM. The user's key can be
// unusable where vmm runs (a passphrase with no ssh-agent, as under
// vmm-web's systemd unit or plain sudo), and the managed key still works.
func SSHKeys(userKey, sshDir string) []string {
	managed := sshkey.PrivateKeyPath(sshDir)
	if userKey == "" || userKey == managed {
		return []string{managed}
	}
	return []string{userKey, managed}
}

// errNoUsableKey means no ssh-agent key and no key file could be used, which
// retrying won't change.
var errNoUsableKey = errors.New("no usable SSH key")

// signers returns the keys to offer, in order: the ssh-agent's, then each key
// file that can be read and parsed. Key files that can't be used are skipped
// and described in skipped. The caller closes agentConn, if any, once it has
// finished authenticating.
func (s *SSHClient) signers() (signers []ssh.Signer, skipped []string, agentConn net.Conn) {
	// The agent goes first: it holds passphrase-protected keys already unlocked.
	if agentSock := os.Getenv("SSH_AUTH_SOCK"); agentSock != "" {
		if conn, err := net.Dial("unix", agentSock); err == nil {
			if agentSigners, err := agent.NewClient(conn).Signers(); err == nil && len(agentSigners) > 0 {
				signers = append(signers, agentSigners...)
				agentConn = conn
			} else {
				_ = conn.Close()
			}
		}
	}
	for _, path := range s.KeyPaths {
		keyData, err := os.ReadFile(path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			var passErr *ssh.PassphraseMissingError
			if errors.As(err, &passErr) {
				skipped = append(skipped, path+": passphrase protected (load it into an ssh-agent to use it)")
			} else {
				skipped = append(skipped, fmt.Sprintf("%s: %v", path, err))
			}
			continue
		}
		signers = append(signers, signer)
	}
	return signers, skipped, agentConn
}

func (s *SSHClient) Connect() error {
	signers, skipped, agentConn := s.signers()
	if agentConn != nil {
		defer func() { _ = agentConn.Close() }()
	}
	if len(signers) == 0 {
		return fmt.Errorf("%w (%s)", errNoUsableKey, strings.Join(skipped, "; "))
	}
	config := &ssh.ClientConfig{
		User: s.User,
		// One method holding every key: x/crypto/ssh tries each auth method
		// type once, so a second "publickey" method would never be offered
		// after the first is refused.
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	port := s.port
	if port == "" {
		port = "22"
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(s.IP, port), config)
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

// WaitForSSH connects to ip with keyPaths (see SSHKeys), retrying until the
// VM's sshd answers or timeout passes. It gives up at once if none of the
// keys can be used, since waiting won't fix that.
func WaitForSSH(ip string, keyPaths []string, timeout time.Duration) (*SSHClient, error) {
	deadline := time.Now().Add(timeout)
	client := NewSSHClient(ip, keyPaths)
	var lastErr error
	for time.Now().Before(deadline) {
		err := client.Connect()
		if errors.Is(err, errNoUsableKey) {
			return nil, fmt.Errorf("SSH to %s: %w", ip, err)
		}
		if err == nil {
			if _, err := client.Run("echo ready"); err == nil {
				return client, nil
			} else {
				lastErr = fmt.Errorf("run failed: %w", err)
			}
			client.Close()
		} else {
			lastErr = fmt.Errorf("connect failed: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("SSH to %s not available after %s (last error: %w, keys: %s)", ip, timeout, lastErr, strings.Join(keyPaths, ", "))
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
		installNodeMountsUnit,
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
	cmd := fmt.Sprintf(`hostnamectl set-hostname %s 2>/dev/null; grep -q "^127.0.0.1" /etc/hosts || echo "127.0.0.1 localhost" >> /etc/hosts; echo "%s %s" >> /etc/hosts`, hostname, ip, hostname)
	_, err := client.Run(cmd)
	return err
}

func initControlPlane(client *SSHClient, cl *Cluster) error {
	if err := setHostname(client, cl.ControlPlaneVM, cl.ControlPlaneIP); err != nil {
		return fmt.Errorf("failed to set hostname: %w", err)
	}

	kubeadmArgs := fmt.Sprintf(
		"kubeadm init --kubernetes-version=%s --pod-network-cidr=%s --service-cidr=%s --apiserver-advertise-address=%s --node-name=%s --ignore-preflight-errors=SystemVerification ",
		cl.K8sVersion, cl.PodSubnet, cl.ServiceSubnet, cl.ControlPlaneIP, cl.ControlPlaneVM,
	)
	if cl.CNI == CNICilium || cl.CNI == "" {
		kubeadmArgs += "--skip-phases=addon/kube-proxy "
	}
	cmd := kubeadmArgs
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

func installCalico(client *SSHClient, podCIDR string) error {
	commands := []string{
		"export KUBECONFIG=/etc/kubernetes/admin.conf && " +
			`kubectl create -f https://raw.githubusercontent.com/projectcalico/calico/v3.29.3/manifests/tigera-operator.yaml`,
		`export KUBECONFIG=/etc/kubernetes/admin.conf && ` +
			`for i in $(seq 1 60); do ` +
			`kubectl get crd installations.operator.tigera.io apiservers.operator.tigera.io >/dev/null 2>&1 && break; ` +
			`sleep 2; done && ` +
			`kubectl wait --for condition=established crd/installations.operator.tigera.io crd/apiservers.operator.tigera.io --timeout=60s`,
		fmt.Sprintf(`export KUBECONFIG=/etc/kubernetes/admin.conf && cat <<'EOF' | kubectl create -f -
apiVersion: operator.tigera.io/v1
kind: Installation
metadata:
  name: default
spec:
  calicoNetwork:
    bgp: Disabled
    linuxDataplane: Nftables
    ipPools:
    - cidr: %s
      encapsulation: VXLAN
      natOutgoing: Enabled
      nodeSelector: all()
---
apiVersion: operator.tigera.io/v1
kind: APIServer
metadata:
  name: default
spec: {}
EOF`, podCIDR),
	}
	for _, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("calico install failed: %w", err)
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

// installNodeMountsUnit installs and starts vmm-k8s-mounts.service, which
// makes / a shared mount and mounts the BPF filesystem before containerd and
// kubelet start. Cilium needs both; done once over SSH they were lost when a
// node restarted, leaving its pods failing with "/sys/fs/bpf is mounted on
// /sys but it is not a shared or slave mount".
const installNodeMountsUnit = `cat <<'EOF' > /etc/systemd/system/vmm-k8s-mounts.service
# VMM generated: mounts Kubernetes (Cilium) needs, redone at every boot
[Unit]
Description=Shared root and BPF filesystem mounts for Kubernetes
DefaultDependencies=no
After=local-fs.target
Before=containerd.service kubelet.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/mount --make-rshared /
ExecStart=/bin/sh -c 'mountpoint -q /sys/fs/bpf || mount -t bpf bpf /sys/fs/bpf'

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now vmm-k8s-mounts.service`

type NodeInfo struct {
	Name string
	IP   string
}

func checkKubeadmPreinstalled(client *SSHClient) bool {
	_, err := client.Run("which kubeadm")
	return err == nil
}

// installedKubeadmVersion returns the version of the node's kubeadm, e.g. 1.36.5.
func installedKubeadmVersion(client *SSHClient) (string, error) {
	out, err := client.Run("kubeadm version -o short")
	if err != nil {
		return "", fmt.Errorf("failed to read the image's kubeadm version: %w", err)
	}
	v := strings.TrimPrefix(strings.TrimSpace(out), "v")
	if err := validate.K8sVersion(v); err != nil {
		return "", fmt.Errorf("unexpected kubeadm version %q: %w", out, err)
	}
	return v, nil
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
		installNodeMountsUnit,
		"systemctl restart containerd",
	}
	for _, cmd := range commands {
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("prepare step failed: %w\nCommand: %s", err, cmd)
		}
	}
	return nil
}

// checkNodesHaveKVM fails before Kubernetes is installed if a nested
// cluster's nodes can't run VMs. vmm can't tell from a kernel file whether it
// was built with KVM, and k8s-kernel releases only have it from v0.17.0's
// profile on, so an older k8s-kernel boots fine but leaves the nodes without
// /dev/kvm.
func checkNodesHaveKVM(clients map[string]*SSHClient, nodes []NodeInfo, kernel string) error {
	fmt.Println("Checking the nodes can run VMs (/dev/kvm)...")
	for _, node := range nodes {
		if _, err := clients[node.Name].Run("test -c /dev/kvm"); err != nil {
			return fmt.Errorf("node %s has no /dev/kvm, so its kernel (%s) was built without KVM; "+
				"get a current one with 'sudo vmm kernel pull k8s-kernel --force' and recreate the cluster", node.Name, kernel)
		}
	}
	return nil
}

func ProvisionCluster(cl *Cluster, sshKeys []string, nodes []NodeInfo) error {
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes to provision")
	}

	if cl.Distro == DistroOpenShift {
		return provisionMicroShift(cl, sshKeys, nodes[0])
	}

	cpNode := nodes[0]
	workerNodes := nodes[1:]
	cl.ControlPlaneIP = cpNode.IP

	// Wait for SSH on all nodes
	fmt.Println("Waiting for VMs to be SSH-accessible...")
	clients := make(map[string]*SSHClient)
	for _, node := range nodes {
		fmt.Printf("  Waiting for %s (%s)...\n", node.Name, node.IP)
		client, err := WaitForSSH(node.IP, sshKeys, 180*time.Second)
		if err != nil {
			return fmt.Errorf("SSH to %s failed: %w", node.Name, err)
		}
		clients[node.Name] = client
		defer client.Close()
	}
	fmt.Println("All VMs are SSH-accessible.")

	if cl.Nested {
		if err := checkNodesHaveKVM(clients, nodes, cl.Kernel); err != nil {
			return err
		}
	}

	// Check if kubeadm is pre-installed (k8s rootfs image)
	preinstalled := checkKubeadmPreinstalled(clients[cpNode.Name])

	if preinstalled {
		fmt.Println("Kubernetes components detected in rootfs image, skipping installation.")
		// kubeadm init must match the installed kubelet, so the image's
		// kubeadm decides the version.
		installed, err := installedKubeadmVersion(clients[cpNode.Name])
		if err != nil {
			return err
		}
		if installed != cl.K8sVersion {
			if cl.K8sVersion != "" {
				fmt.Printf("Image has Kubernetes %s, not %s; using %s.\n", installed, cl.K8sVersion, installed)
			}
			cl.K8sVersion = installed
		}
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
		if cl.K8sVersion == "" {
			return errors.New("the image has no Kubernetes installed; give an exact Kubernetes version to install")
		}
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

	// Install CNI
	cni := cl.CNI
	if cni == "" {
		cni = CNICilium
	}
	switch cni {
	case CNICalico:
		fmt.Println("Installing Calico CNI...")
		if err := installCalico(cpClient, cl.PodSubnet); err != nil {
			return err
		}
		fmt.Println("Calico installed.")
	default:
		fmt.Println("Installing Cilium CNI...")
		if err := installCilium(cpClient, cl.ControlPlaneIP); err != nil {
			return err
		}
		fmt.Println("Cilium installed.")
	}

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

	// For single-node clusters, untaint the control plane so workloads can be scheduled
	if len(workerNodes) == 0 {
		fmt.Println("Single-node cluster: removing control-plane taint...")
		if _, err := cpClient.Run("export KUBECONFIG=/etc/kubernetes/admin.conf && kubectl taint nodes --all node-role.kubernetes.io/control-plane-"); err != nil {
			return fmt.Errorf("failed to untaint control plane: %w", err)
		}
		fmt.Println("Control-plane taint removed.")
	}

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
