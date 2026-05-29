package cluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MicroShift upstream community build (OKD payload images, no Red Hat subscription).
const (
	microShiftRepo = "microshift-io/microshift"
	microShiftAPI  = "https://api.github.com/repos/" + microShiftRepo + "/releases"

	// microShiftKubeconfig is where MicroShift writes the kubeadmin kubeconfig.
	microShiftKubeconfig = "/var/lib/microshift/resources/kubeadmin/kubeconfig"
)

// msRelease is the subset of a GitHub release we need.
type msRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// findMicroShiftDebsURL resolves the download URL for the x86_64 .deb bundle of the
// newest microshift-io release matching the given major.minor version (e.g. "4.20").
// The version is server-resolved from GitHub releases; no client-supplied URLs.
func findMicroShiftDebsURL(ocpVersion string) (url, tag string) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(microShiftAPI)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}

	var releases []msRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", ""
	}

	for _, rel := range releases {
		// Release tags look like "4.20.0_g153ff0ca9_4.20.0_okd_scos.16".
		if !strings.HasPrefix(rel.TagName, ocpVersion+".") && rel.TagName != ocpVersion {
			continue
		}
		for _, a := range rel.Assets {
			if a.Name == "microshift-debs-x86_64.tgz" {
				return a.BrowserDownloadURL, rel.TagName
			}
		}
	}
	return "", ""
}

// k8sVersionForOpenShift maps an OpenShift major.minor (e.g. "4.20") to the
// Kubernetes major.minor it ships (OCP 4.N == Kubernetes 1.(N+13)).
func k8sVersionForOpenShift(ocpVersion string) string {
	parts := strings.SplitN(ocpVersion, ".", 2)
	if len(parts) != 2 {
		return "1.33"
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return "1.33"
	}
	return fmt.Sprintf("1.%d", minor+13)
}

// crioCandidateList returns a space-separated, descending list of CRI-O stable
// channel versions to probe, centered on the cluster's Kubernetes minor. CRI-O
// lags Kubernetes on pkgs.k8s.io, so we accept the newest available within range.
func crioCandidateList(k8sMajorMinor string) string {
	parts := strings.SplitN(k8sMajorMinor, ".", 2)
	minor := 33
	if len(parts) == 2 {
		if m, err := strconv.Atoi(parts[1]); err == nil {
			minor = m
		}
	}
	var versions []string
	for v := minor + 1; v >= minor-3; v-- {
		versions = append(versions, fmt.Sprintf("v1.%d", v))
	}
	return strings.Join(versions, " ")
}

// ensureLocalhostHosts writes loopback entries into /etc/hosts. The base Ubuntu
// rootfs ships an empty /etc/hosts, which makes etcd fail with "lookup localhost:
// no such host". Idempotent.
func ensureLocalhostHosts(client *SSHClient, hostname string) error {
	cmd := fmt.Sprintf(`if ! grep -qE '^127\.0\.0\.1[[:space:]]+localhost' /etc/hosts; then
{ echo "127.0.0.1 localhost localhost.localdomain"; echo "::1 localhost localhost.localdomain"; echo "127.0.1.1 %s"; cat /etc/hosts; } > /etc/hosts.new
mv /etc/hosts.new /etc/hosts
fi`, hostname)
	_, err := client.Run(cmd)
	return err
}

// installMicroShift installs CRI-O, kubectl and the MicroShift .debs, applies the
// fixes discovered for the Firecracker/Ubuntu environment, and starts the service.
func installMicroShift(client *SSHClient, cl *Cluster, debsURL string) error {
	k8sMajorMinor := k8sVersionForOpenShift(cl.OpenShiftVer)
	crioCandidates := crioCandidateList(k8sMajorMinor)

	// 1. Prerequisites.
	prereq := "DEBIAN_FRONTEND=noninteractive apt-get update -qq && " +
		"DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl gnupg jq kmod ca-certificates apt-transport-https"
	if _, err := client.Run(prereq); err != nil {
		return fmt.Errorf("microshift prerequisites failed: %w", err)
	}

	// 2. Add CRI-O + Kubernetes apt repos and install. CRI-O stable on pkgs.k8s.io
	//    lags Kubernetes, so probe candidates and use the newest available.
	repoInstall := fmt.Sprintf(`set -e
export DEBIAN_FRONTEND=noninteractive
mkdir -p /etc/apt/keyrings
K8S_VER=v%s
CRIO_VER=""
for v in %s; do
  if curl -fsSL -o /dev/null "https://pkgs.k8s.io/addons:/cri-o:/stable:/$v/deb/Release.key"; then CRIO_VER=$v; break; fi
done
if [ -z "$CRIO_VER" ]; then echo "no CRI-O stable repo found for candidates: %s" >&2; exit 1; fi
echo "Using CRI-O $CRIO_VER, Kubernetes $K8S_VER"
curl -fsSL "https://pkgs.k8s.io/core:/stable:/$K8S_VER/deb/Release.key" | gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes.gpg] https://pkgs.k8s.io/core:/stable:/$K8S_VER/deb/ /" > /etc/apt/sources.list.d/kubernetes.list
curl -fsSL "https://pkgs.k8s.io/addons:/cri-o:/stable:/$CRIO_VER/deb/Release.key" | gpg --dearmor --yes -o /etc/apt/keyrings/cri-o.gpg
echo "deb [signed-by=/etc/apt/keyrings/cri-o.gpg] https://pkgs.k8s.io/addons:/cri-o:/stable:/$CRIO_VER/deb/ /" > /etc/apt/sources.list.d/cri-o.list
apt-get update -qq
apt-get install -y -qq cri-o crun containernetworking-plugins kubectl cri-tools`, k8sMajorMinor, crioCandidates, crioCandidates)
	if _, err := client.Run(repoInstall); err != nil {
		return fmt.Errorf("CRI-O/kubectl install failed: %w", err)
	}

	// 3. Point CRI-O at the CNI plugin binaries. MicroShift's CRI-O defaults to
	//    plugin_dirs=["/usr/libexec/cni"], which is empty on Ubuntu; the binaries
	//    ship in /opt/cni/bin and /usr/lib/cni. Without this the node stays NotReady
	//    ("no CNI configuration file in /etc/cni/net.d").
	crioCNI := `mkdir -p /etc/crio/crio.conf.d
cat > /etc/crio/crio.conf.d/20-cni-plugin-dirs.conf <<'CONF'
[crio.network]
plugin_dirs = [
    "/opt/cni/bin",
    "/usr/lib/cni",
    "/usr/libexec/cni",
]
CONF
systemctl enable --now crio`
	if _, err := client.Run(crioCNI); err != nil {
		return fmt.Errorf("CRI-O CNI configuration failed: %w", err)
	}

	// 4. Download and install the MicroShift .deb bundle (OKD payload images).
	debInstall := fmt.Sprintf(`set -e
export DEBIAN_FRONTEND=noninteractive
cd /tmp
curl -sSL --fail -o microshift-debs.tgz "%s"
rm -rf /tmp/ms-debs && mkdir -p /tmp/ms-debs
tar xzf microshift-debs.tgz -C /tmp/ms-debs
apt-get install -y -qq /tmp/ms-debs/microshift_*.deb /tmp/ms-debs/microshift-kindnet*.deb /tmp/ms-debs/microshift-release-info*.deb
rm -f /tmp/microshift-debs.tgz`, debsURL)
	if _, err := client.Run(debInstall); err != nil {
		return fmt.Errorf("MicroShift package install failed: %w", err)
	}

	// 5. Write MicroShift config so the API server cert is valid for the node IP
	//    (lets the host reach the cluster), then start MicroShift.
	config := fmt.Sprintf(`mkdir -p /etc/microshift
cat > /etc/microshift/config.yaml <<'CONF'
apiServer:
  subjectAltNames:
  - %s
node:
  nodeIP: %s
CONF
systemctl enable --now microshift`, cl.ControlPlaneIP, cl.ControlPlaneIP)
	if _, err := client.Run(config); err != nil {
		return fmt.Errorf("MicroShift start failed: %w", err)
	}

	return nil
}

// waitForMicroShiftReady polls until the MicroShift node reports Ready.
func waitForMicroShiftReady(client *SSHClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	kc := "KUBECONFIG=" + microShiftKubeconfig
	for time.Now().Before(deadline) {
		out, err := client.Run(fmt.Sprintf("test -f %s && %s kubectl get nodes --no-headers 2>/dev/null", microShiftKubeconfig, kc))
		if err == nil {
			line := strings.TrimSpace(out)
			if line != "" && strings.Contains(line, " Ready") && !strings.Contains(line, "NotReady") {
				return nil
			}
		}
		time.Sleep(10 * time.Second)
	}
	// Surface recent logs to aid debugging on timeout.
	logs, _ := client.Run("journalctl -u microshift --no-pager -n 20 2>/dev/null | tail -20")
	return fmt.Errorf("timed out waiting for MicroShift node to be Ready after %s\nRecent logs:\n%s", timeout, logs)
}

// provisionMicroShift stands up a single-node OpenShift-derived cluster via MicroShift.
func provisionMicroShift(cl *Cluster, sshKeyPath string, node NodeInfo) error {
	cl.ControlPlaneIP = node.IP

	debsURL, tag := findMicroShiftDebsURL(cl.OpenShiftVer)
	if debsURL == "" {
		return fmt.Errorf("no MicroShift %s release with x86_64 .deb bundle found at github.com/%s", cl.OpenShiftVer, microShiftRepo)
	}
	fmt.Printf("Using MicroShift release: %s\n", tag)

	fmt.Printf("Waiting for %s (%s) to be SSH-accessible...\n", node.Name, node.IP)
	client, err := WaitForSSH(node.IP, sshKeyPath, 180*time.Second)
	if err != nil {
		return fmt.Errorf("SSH to %s failed: %w", node.Name, err)
	}
	defer client.Close()

	if err := setHostname(client, node.Name, node.IP); err != nil {
		return fmt.Errorf("failed to set hostname: %w", err)
	}
	if err := ensureLocalhostHosts(client, node.Name); err != nil {
		return fmt.Errorf("failed to configure /etc/hosts: %w", err)
	}

	fmt.Println("Installing MicroShift (CRI-O + OpenShift control plane)...")
	if err := installMicroShift(client, cl, debsURL); err != nil {
		return err
	}

	fmt.Println("Waiting for OpenShift control plane to become Ready (pulling images, may take several minutes)...")
	if err := waitForMicroShiftReady(client, 12*time.Minute); err != nil {
		return err
	}
	fmt.Println("OpenShift node is Ready.")
	return nil
}
