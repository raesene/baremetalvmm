# OpenShift Clusters

VMM can create single-node, OpenShift-derived clusters from a Firecracker VM using
upstream [MicroShift](https://github.com/microshift-io/microshift). MicroShift bundles
the OpenShift control plane (kube-apiserver, etcd, kubelet, CRI-O) into a single
systemd service. The upstream community build uses OKD payload images, so **no Red Hat
subscription or pull secret is required**.

This is the only OpenShift variant that fits the Firecracker model. Full OKD/OCP
requires Fedora/RHCOS immutable images booted via Ignition (plus PXE and BMC/Redfish
for installer-provisioned installs), none of which exist in VMM's boot path. OpenShift
Local / CRC ships and manages its own VM, which would mean nested virtualization.

## Characteristics and limitations

- **Single-node only.** MicroShift is single-node by design; `--workers` is ignored for
  OpenShift clusters.
- **Headless.** MicroShift does not ship the OpenShift web console. Manage the cluster
  with `kubectl` / `oc`.
- **Experimental.** The upstream Ubuntu build is community-maintained.
- Provides genuine OpenShift APIs: `route.openshift.io`, `security.openshift.io`
  (SecurityContextConstraints), ingress router, service-ca, and OLM.

## Prerequisites

MicroShift is installed onto the base Ubuntu 24.04 rootfs over SSH at create time
(install-on-provision); no dedicated pre-built image is needed. By default VMM uses the
`security-kernel` (6.12 LTS, broad module coverage), which provides the kernel modules
CRI-O and the kindnet CNI require. It falls back to `k8s-kernel` if `security-kernel` is
not present.

As with Kubernetes clusters, VMM uses its managed Ed25519 SSH key for provisioning when
no `--ssh-key` is provided.

## Creating a cluster

```bash
# Single-node OpenShift cluster with defaults
sudo vmm cluster create myocp --type openshift

# Pin a specific OpenShift/MicroShift major.minor version
sudo vmm cluster create myocp --type openshift --openshift-version 4.20

# With custom resources
sudo vmm cluster create myocp --type openshift \
  --cpus 6 --memory 12288 --disk 30720
```

OpenShift clusters use heavier defaults than kubeadm clusters: **4 CPUs, 8192 MB memory,
20480 MB disk** (minimums enforced: 2 CPUs, 4096 MB, 10240 MB).

The create command:
1. Creates a single Firecracker VM (`{name}-control-plane`).
2. Configures `/etc/hosts`, installs CRI-O + `kubectl` from `pkgs.k8s.io`, and points
   CRI-O at the host's CNI plugin directories.
3. Downloads and installs the MicroShift `.deb` bundle (OKD payload images) for the
   requested version, resolved from the `microshift-io/microshift` releases.
4. Writes `/etc/microshift/config.yaml` with the node IP as a certificate
   subjectAltName, then starts `microshift.service`.
5. Waits for the node to become `Ready` (first boot pulls all images and can take
   several minutes).
6. Merges the kubeconfig into `~/.kube/config` as context `vmm-{name}`.

## Using a cluster

```bash
kubectl --context vmm-myocp get nodes
kubectl --context vmm-myocp get pods -A

# OpenShift-specific resources
kubectl --context vmm-myocp get routes -A
kubectl --context vmm-myocp get securitycontextconstraints
```

If you created the cluster with `--admin-workstation`, the kubeconfig is also copied to
`/root/.kube/config` on the `{name}-admin` VM.

To re-merge the kubeconfig later:

```bash
sudo vmm cluster kubeconfig myocp
```

## Deleting a cluster

```bash
sudo vmm cluster delete myocp -f
```

## How it works

The OpenShift path lives in `internal/cluster/openshift.go` and is selected by the
`Distro` field on the cluster (`kubeadm` by default, `openshift` for MicroShift).
`ProvisionCluster` branches to `provisionMicroShift`, which installs and starts
MicroShift over SSH on the single node. Networking on Ubuntu uses the **kindnet** CNI
(OVN-Kubernetes is not available on Ubuntu, as it requires the `openvswitch` kernel
module).
