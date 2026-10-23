#!/bin/bash
#
# distro-test.sh: run vmm on stock Linux distributions, each in a throwaway
# QEMU/KVM VM booted from the distro's official cloud image.
#
#   scripts/distro-test.sh run fedora-44              # boot, install, smoke test, discard
#   scripts/distro-test.sh run ubuntu-24.04 debian-13 # several, one after another
#   scripts/distro-test.sh up rocky-10 && scripts/distro-test.sh ssh rocky-10
#
# Run it on a KVM host with nested virtualization (vmm's Firecracker VMs run
# inside the test VM). Needs qemu-system-x86, qemu-utils and cloud-image-utils
# (cloud-localds), and access to /dev/kvm (the kvm group).
#
# Each test VM is a qcow2 overlay on an untouched, checksum-verified base image,
# so every 'up' starts from a clean install. Networking is QEMU user mode: no
# bridges, TAPs or firewall rules are added to the host, which matters because
# the host's own firewall is part of what vmm gets tested against. The VM's SSH
# port and a port-forward test port are published on 127.0.0.1 only.
#
# Cloud images are not quite installed systems, so each profile adds what a
# normal install of that distro would have (firewalld on Fedora and the EL
# rebuilds, for instance).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR="${DISTRO_TEST_DIR:-/var/tmp/vmm-distro-test}"
GITHUB_REPO="raesene/baremetalvmm"
GUEST_USER="tester"
# Guest port the smoke test forwards to its inner VM's SSH, published on the host.
PF_GUEST_PORT=8022

CPUS=4
MEMORY=6144
DISK=40G
VERSION=""
BINARIES=""
KEEP=0

DISTROS="ubuntu-24.04 ubuntu-26.04 debian-13 fedora-44 rocky-10 alma-10"

usage() {
    cat <<EOF
Usage: distro-test.sh <command> [options] [distro...]

Commands:
  list                  Show distro profiles and which test VMs are running
  up <distro>           Boot a clean VM from the distro's cloud image
  install <distro>      Install vmm in the VM with scripts/install.sh
  smoke <distro>        Run the vmm smoke test in the VM
  ssh <distro> [cmd]    SSH into the VM
  down <distro>         Stop the VM and discard its disk
  run <distro>...       up + install + smoke + down, for each distro in turn

Options:
  --version X           vmm release to install (default: latest)
  --binaries DIR        After installing, replace vmm and vmm-web with the
                        binaries in DIR (to test an unreleased build)
  --keep                run: leave the VM up afterwards
  --cpus N              vCPUs per test VM (default $CPUS)
  --memory MB           Memory per test VM (default $MEMORY)
  --disk SIZE           Disk size per test VM (default $DISK)

Distros: $DISTROS
State (images, keys, VM disks and logs): $STATE_DIR (set DISTRO_TEST_DIR to change)
EOF
}

say()  { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'Error: %s\n' "$*" >&2; exit 1; }

# ---------- Distro profiles ----------

# profile DISTRO sets IMG_URL, SUM_URL and PREP (shell run as root
# by cloud-init to make the cloud image look like a normal install).
profile() {
    PREP=""
    case "$1" in
        ubuntu-24.04)
            IMG_URL="https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
            SUM_URL="https://cloud-images.ubuntu.com/noble/current/SHA256SUMS" ;;
        ubuntu-26.04)
            IMG_URL="https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img"
            SUM_URL="https://cloud-images.ubuntu.com/resolute/current/SHA256SUMS" ;;
        debian-13)
            IMG_URL="https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
            SUM_URL="https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS" ;;
        fedora-44)
            # Fedora's image names carry a compose number, so find the current one.
            local base="https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images"
            local listing img sum
            listing=$(curl -fsSL --retry 3 "$base/") || die "could not list $base"
            img=$(grep -oE 'Fedora-Cloud-Base-Generic-44-[0-9.]+\.x86_64\.qcow2' <<<"$listing" | sort -uV | tail -1)
            sum=$(grep -oE 'Fedora-Cloud-44-[0-9.]+-x86_64-CHECKSUM' <<<"$listing" | sort -uV | tail -1)
            [ -n "$img" ] && [ -n "$sum" ] || die "no Fedora 44 cloud image found in $base"
            IMG_URL="$base/$img"
            SUM_URL="$base/$sum"
            # Fedora Server and Workstation run firewalld; the cloud image doesn't ship it.
            PREP="dnf -y install firewalld && systemctl enable --now firewalld" ;;
        rocky-10)
            IMG_URL="https://dl.rockylinux.org/pub/rocky/10/images/x86_64/Rocky-10-GenericCloud-Base.latest.x86_64.qcow2"
            SUM_URL="$IMG_URL.CHECKSUM"
            PREP="dnf -y install firewalld && systemctl enable --now firewalld" ;;
        alma-10)
            IMG_URL="https://repo.almalinux.org/almalinux/10/cloud/x86_64/images/AlmaLinux-10-GenericCloud-latest.x86_64.qcow2"
            SUM_URL="https://repo.almalinux.org/almalinux/10/cloud/x86_64/images/CHECKSUM"
            PREP="dnf -y install firewalld && systemctl enable --now firewalld" ;;
        *) die "unknown distro '$1' (known: $DISTROS)" ;;
    esac
}

# vm_paths DISTRO sets VM_DIR and the published ports, which follow the
# distro's position in $DISTROS.
vm_paths() {
    VM_DIR="$STATE_DIR/vms/$1"
    case " $DISTROS " in *" $1 "*) ;; *) die "unknown distro '$1' (known: $DISTROS)" ;; esac
    local i=1 d
    for d in $DISTROS; do
        [ "$d" = "$1" ] && break
        i=$((i + 1))
    done
    SSH_PORT=$((2220 + i))
    PF_PORT=$((2320 + i))
}

# ---------- Helpers ----------

vm_running() {
    [ -f "$VM_DIR/qemu.pid" ] && kill -0 "$(cat "$VM_DIR/qemu.pid")" 2>/dev/null
}

ssh_key() {
    local key="$STATE_DIR/keys/id_ed25519"
    if [ ! -f "$key" ]; then
        mkdir -p "$STATE_DIR/keys"
        chmod 700 "$STATE_DIR/keys"
        ssh-keygen -q -t ed25519 -N "" -C "vmm-distro-test" -f "$key"
    fi
    echo "$key"
}

SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR
          -o ConnectTimeout=5 -o ServerAliveInterval=15)

guest() {
    ssh "${SSH_OPTS[@]}" -i "$(ssh_key)" -p "$SSH_PORT" "$GUEST_USER@127.0.0.1" "$@"
}

guest_copy() {
    scp -q "${SSH_OPTS[@]}" -i "$(ssh_key)" -P "$SSH_PORT" "$@"
}

need() {
    local tool
    for tool in "$@"; do
        command -v "$tool" >/dev/null 2>&1 || die "$tool is required (apt install qemu-system-x86 qemu-utils cloud-image-utils)"
    done
}

# fetch_image: download the profile's base image unless the cached copy still
# matches the published checksum. Prints the image path.
fetch_image() {
    local name sums expected algo img
    name=$(basename "$IMG_URL")
    img="$STATE_DIR/images/$name"
    mkdir -p "$STATE_DIR/images"
    sums=$(curl -fsSL --retry 3 "$SUM_URL") || die "could not download $SUM_URL"
    # Checksum files come in GNU ("hash  file") and BSD ("SHA256 (file) = hash")
    # forms; take the hash on the line naming our file either way.
    expected=$(grep -F "$name" <<<"$sums" | grep -v '^#' | grep -oE '\b[0-9a-f]{64}([0-9a-f]{64})?\b' | head -1)
    [ -n "$expected" ] || die "$SUM_URL has no checksum for $name"
    case ${#expected} in
        64) algo=sha256sum ;;
        128) algo=sha512sum ;;
    esac
    if [ -f "$img" ] && [ "$(cat "$img.sum" 2>/dev/null)" = "$expected" ]; then
        echo "$img"
        return
    fi
    say "Downloading $name..." >&2
    local progress=-sS
    [ -t 2 ] && progress=--progress-bar
    curl -fL --retry 3 "$progress" -o "$img.part" "$IMG_URL" >&2 || die "download failed: $IMG_URL"
    [ "$($algo "$img.part" | awk '{print $1}')" = "$expected" ] || { rm -f "$img.part"; die "checksum mismatch for $name"; }
    mv -f "$img.part" "$img"
    echo "$expected" > "$img.sum"
    echo "$img"
}

wait_for_ssh() {
    local deadline=$((SECONDS + 300))
    until guest true 2>/dev/null; do
        vm_running || die "VM exited during boot; see $VM_DIR/console.log"
        [ $SECONDS -lt $deadline ] || die "no SSH after 5 minutes; see $VM_DIR/console.log"
        sleep 3
    done
    # cloud-init exits 2 for recoverable errors (deprecated keys and the like).
    local rc=0
    guest sudo cloud-init status --wait >/dev/null 2>&1 || rc=$?
    case $rc in
        0) ;;
        2) warn "cloud-init finished with recoverable errors (guest: sudo cloud-init status --long)" ;;
        *) die "cloud-init failed (guest: sudo cloud-init status --long)" ;;
    esac
}

latest_version() {
    # The releases/latest redirect, not the API: the API's 60/hr limit is shared.
    curl -fsSI "https://github.com/$GITHUB_REPO/releases/latest" \
        | sed -n 's|^[Ll]ocation: .*/tag/v\([^[:space:]]*\).*|\1|p' | tail -1
}

# ---------- Commands ----------

cmd_list() {
    local d
    printf '%-14s %-8s %s\n' DISTRO STATE SSH
    for d in $DISTROS; do
        vm_paths "$d"
        if vm_running; then
            printf '%-14s %-8s %s\n' "$d" running "$0 ssh $d  (127.0.0.1:$SSH_PORT)"
        else
            printf '%-14s %-8s\n' "$d" -
        fi
    done
}

cmd_up() {
    local d="$1" base fmt key
    need qemu-system-x86_64 qemu-img cloud-localds curl ssh
    [ -r /dev/kvm ] && [ -w /dev/kvm ] || die "no access to /dev/kvm (sudo usermod -aG kvm $USER, then log in again)"
    profile "$d"
    vm_paths "$d"
    vm_running && die "$d is already running (down it first)"

    base=$(fetch_image)
    key=$(ssh_key)
    rm -rf "$VM_DIR"
    mkdir -p "$VM_DIR"

    # Ubuntu's .img is qcow2 too, but don't assume: a wrong backing format boots nothing.
    fmt=$(qemu-img info "$base" | sed -n 's/^file format: //p')
    qemu-img create -q -f qcow2 -F "$fmt" -b "$base" "$VM_DIR/disk.qcow2" "$DISK"

    cat > "$VM_DIR/meta-data" <<EOF
instance-id: vmmtest-$d-$(date +%s)
local-hostname: vmmtest-${d//./-}
EOF
    {
        echo "#cloud-config"
        echo "users:"
        echo "  - name: $GUEST_USER"
        echo "    sudo: ALL=(ALL) NOPASSWD:ALL"
        echo "    shell: /bin/bash"
        echo "    ssh_authorized_keys:"
        echo "      - $(cat "$key.pub")"
        echo "ssh_pwauth: false"
        if [ -n "$PREP" ]; then
            echo "runcmd:"
            echo "  - [sh, -c, \"$PREP\"]"
        fi
    } > "$VM_DIR/user-data"
    cloud-localds "$VM_DIR/seed.iso" "$VM_DIR/user-data" "$VM_DIR/meta-data"

    say "Booting $d (ssh on 127.0.0.1:$SSH_PORT)..."
    qemu-system-x86_64 \
        -name "vmmtest-$d" \
        -machine q35,accel=kvm -cpu host -smp "$CPUS" -m "$MEMORY" \
        -drive "file=$VM_DIR/disk.qcow2,if=virtio,format=qcow2" \
        -drive "file=$VM_DIR/seed.iso,if=virtio,format=raw,readonly=on" \
        -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22,hostfwd=tcp:127.0.0.1:$PF_PORT-:$PF_GUEST_PORT" \
        -device virtio-net-pci,netdev=n0 \
        -device virtio-rng-pci \
        -display none -serial "file:$VM_DIR/console.log" \
        -pidfile "$VM_DIR/qemu.pid" -daemonize
    wait_for_ssh
    # If setup installed a newer kernel (on EL10 that's the one with
    # kernel-modules-extra, where iptables' MASQUERADE lives), boot it, as a
    # maintained system would have. Testing the image's stale kernel against
    # modules it doesn't have only finds the cloud image's quirks.
    if [ "$(guest 'ls /lib/modules | sort -V | tail -1')" != "$(guest uname -r)" ]; then
        say "Rebooting $d into its newest kernel..."
        guest sudo systemctl reboot >/dev/null 2>&1 || true
        sleep 10
        wait_for_ssh
    fi
    say "$d is up:$(guest '. /etc/os-release; echo "$PRETTY_NAME, kernel $(uname -r)"')"
}

cmd_install() {
    local d="$1" version="$VERSION"
    vm_paths "$d"
    vm_running || die "$d is not running"
    [ -n "$version" ] || version=$(latest_version)
    [ -n "$version" ] || die "could not find the latest release; pass --version"
    version="${version#v}"

    guest_copy "$SCRIPT_DIR/install.sh" "$GUEST_USER@127.0.0.1:/tmp/install.sh"
    say "Installing vmm $version on $d..."
    # Images are pulled by the smoke test, which checks that step on its own.
    guest sudo bash /tmp/install.sh --version "$version" --no-images
    if [ -n "$BINARIES" ]; then
        [ -x "$BINARIES/vmm" ] && [ -x "$BINARIES/vmm-web" ] || die "$BINARIES needs vmm and vmm-web binaries"
        guest_copy "$BINARIES/vmm" "$BINARIES/vmm-web" "$GUEST_USER@127.0.0.1:/tmp/"
        guest sudo install -m 0755 /tmp/vmm /tmp/vmm-web /usr/local/bin/
        say "Replaced vmm with $BINARIES/vmm: $(guest vmm version | head -1)"
    fi
}

# The guest half of the smoke test, run as root in the test VM. Prints a
# PASS/FAIL line per check and exits non-zero if anything failed. "setup"
# leaves an inner VM running with port $PF_GUEST_PORT forwarded to its SSH so
# the host can check that from outside; "finish" does the rest and cleans up.
SMOKE_GUEST=$(cat <<'GUEST'
set -u
PF_GUEST_PORT="$2"
FAILED=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; FAILED=1; }
check() { local name="$1"; shift; if out=$("$@" 2>&1); then pass "$name"; else fail "$name: $(tail -3 <<<"$out" | tr '\n' ' ')"; fi; }

inner() { timeout 20 vmm ssh "$1" -- "${@:2}" </dev/null; }
wait_inner() {
    local i
    for i in $(seq 1 30); do inner "$1" true >/dev/null 2>&1 && return 0; sleep 3; done
    return 1
}
inner_ip() { sed -n 's/.*"ip_address": *"\([^"]*\)".*/\1/p' "/var/lib/vmm/vms/$1.json"; }
tcp_ok() { inner "$1" "timeout 5 bash -c '</dev/tcp/$2/$3'"; }

case "$1" in
setup)
    # Users run 'sudo vmm', so sudo's secure_path must reach /usr/local/bin
    # (EL distros leave it out). Check, then carry on with the full path.
    if command -v vmm >/dev/null; then pass "sudo finds vmm"; else fail "sudo finds vmm: /usr/local/bin is not in sudo's secure_path"; fi
    export PATH="/usr/local/sbin:/usr/local/bin:$PATH"
    echo "INFO $(. /etc/os-release; echo "$PRETTY_NAME"), kernel $(uname -r), $(vmm version 2>/dev/null | head -1)"
    command -v getenforce >/dev/null && echo "INFO SELinux $(getenforce)"
    systemctl is-active --quiet firewalld && echo "INFO firewalld active, default zone $(firewall-cmd --get-default-zone)"
    echo "INFO iptables $(iptables --version 2>/dev/null || echo missing), nft $(command -v nft >/dev/null && echo present || echo missing)"
    [ -w /dev/kvm ] && pass "/dev/kvm present" || fail "/dev/kvm present: missing (nested virtualization off?)"
    check "image pull" vmm image pull
    check "create" vmm create dt1 --cpus 1 --memory 512 --no-autostart
    check "start" vmm start dt1
    if wait_inner dt1; then pass "ssh to inner VM"; else fail "ssh to inner VM: no answer after 90s"; timeout 5 vmm console dt1 -n 20 | sed 's/^/  console: /'; fi
    ip=$(inner_ip dt1)
    check "ping inner VM ($ip)" ping -c 2 -W 2 "$ip"
    check "inner VM DNS" inner dt1 getent hosts github.com
    check "inner VM egress (1.1.1.1:443)" tcp_ok dt1 1.1.1.1 443
    check "port-forward add" vmm port-forward add dt1 "$PF_GUEST_PORT:22"
    # Not 127.0.0.1: vmm leaves loopback alone (DNAT from it needs route_localnet).
    own=$(ip -4 route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')
    check "port-forward from the host itself ($own)" timeout 5 bash -c "exec 3<>/dev/tcp/$own/$PF_GUEST_PORT && head -c 7 <&3 | grep -q SSH-2.0"
    ;;
finish)
    export PATH="/usr/local/sbin:/usr/local/bin:$PATH"
    check "port-forward remove" vmm port-forward remove dt1 "$PF_GUEST_PORT:22"
    if iptables -t nat -S 2>/dev/null | grep -q -- "--dport $PF_GUEST_PORT"; then fail "port-forward rules removed: still in nat table"; else pass "port-forward rules removed"; fi
    # Restricted VM: internet yes, host and private ranges no. dt1 is the control.
    check "create restricted" vmm create dt2 --cpus 1 --memory 512 --no-autostart --restrict-local-networks
    check "start restricted" vmm start dt2
    if wait_inner dt2; then
        pass "ssh to restricted VM"
        check "restricted VM egress (1.1.1.1:443)" tcp_ok dt2 1.1.1.1 443
        check "unrestricted VM reaches host (control)" tcp_ok dt1 172.16.0.1 22
        if tcp_ok dt2 172.16.0.1 22 >/dev/null 2>&1; then fail "restricted VM blocked from host: connected"; else pass "restricted VM blocked from host"; fi
        if tcp_ok dt2 "$(inner_ip dt1)" 22 >/dev/null 2>&1; then fail "restricted VM blocked from other VM: connected"; else pass "restricted VM blocked from other VM"; fi
    else
        fail "ssh to restricted VM: no answer after 90s"
    fi
    check "stop" vmm stop dt1
    check "stop restricted" vmm stop dt2
    check "delete" vmm delete dt1 -f
    check "delete restricted" vmm delete dt2 -f
    if pgrep -x firecracker >/dev/null; then fail "no firecracker left: $(pgrep -ax firecracker)"; else pass "no firecracker left"; fi
    ;;
esac
exit $FAILED
GUEST
)

cmd_smoke() {
    local d="$1" log rc=0
    vm_paths "$d"
    vm_running || die "$d is not running"
    log="$VM_DIR/smoke.log"
    say "Smoke testing vmm on $d (log: $log)..."
    (
        rc=0
        guest sudo bash -s setup "$PF_GUEST_PORT" <<<"$SMOKE_GUEST" || rc=1
        # From outside the test VM: host -> QEMU -> guest:$PF_GUEST_PORT -> DNAT -> inner VM.
        if timeout 10 bash -c "exec 3<>/dev/tcp/127.0.0.1/$PF_PORT && head -c 7 <&3 | grep -q SSH-2.0" 2>/dev/null; then
            echo "PASS port-forward from outside"
        else
            echo "FAIL port-forward from outside: no SSH banner through 127.0.0.1:$PF_PORT"
            rc=1
        fi
        guest sudo bash -s finish "$PF_GUEST_PORT" <<<"$SMOKE_GUEST" || rc=1
        exit $rc
    ) 2>&1 | tee "$log" || rc=1
    if [ $rc = 0 ]; then say "$d: all checks passed"; else say "$d: FAILED ($(grep -c '^FAIL' "$log") checks)"; fi
    return $rc
}

cmd_ssh() {
    local d="$1"
    shift
    vm_paths "$d"
    vm_running || die "$d is not running"
    guest "$@"
}

cmd_down() {
    local d="$1" pid i
    vm_paths "$d"
    if vm_running; then
        pid=$(cat "$VM_DIR/qemu.pid")
        kill "$pid"
        for i in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
        kill -9 "$pid" 2>/dev/null || true
        say "$d stopped"
    fi
    # Keep the logs from the last run; drop the disk.
    rm -f "$VM_DIR/disk.qcow2" "$VM_DIR/seed.iso" "$VM_DIR/qemu.pid"
}

cmd_run() {
    local d failed=""
    for d in "$@"; do
        vm_paths "$d"
        # Separate processes, so set -e still applies inside each step.
        if "$0" up "$d" "${OPTS[@]}" && "$0" install "$d" "${OPTS[@]}" && "$0" smoke "$d" "${OPTS[@]}"; then
            :
        else
            failed="$failed $d"
        fi
        if [ "$KEEP" = 1 ]; then say "Leaving $d up: $0 ssh $d"; else cmd_down "$d"; fi
    done
    echo
    if [ -n "$failed" ]; then
        say "Failed:$failed (logs in $STATE_DIR/vms/<distro>/)"
        return 1
    fi
    say "Passed: $*"
}

# ---------- Main ----------

[ $# -gt 0 ] || { usage; exit 1; }
COMMAND="$1"
shift
ARGS=()
OPTS=()  # options again, for run's sub-steps
while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="${2:?--version needs a value}"; OPTS+=("$1" "$2"); shift ;;
        --binaries) BINARIES="$(cd "${2:?--binaries needs a directory}" && pwd)"; OPTS+=("$1" "$BINARIES"); shift ;;
        --keep) KEEP=1 ;;
        --cpus) CPUS="${2:?}"; OPTS+=("$1" "$2"); shift ;;
        --memory) MEMORY="${2:?}"; OPTS+=("$1" "$2"); shift ;;
        --disk) DISK="${2:?}"; OPTS+=("$1" "$2"); shift ;;
        -h|--help) usage; exit 0 ;;
        --) shift; ARGS+=("$@"); break ;;
        *) ARGS+=("$1") ;;
    esac
    shift
done
set -- "${ARGS[@]+"${ARGS[@]}"}"

case "$COMMAND" in
    list) cmd_list ;;
    up|install|smoke|down) [ $# -eq 1 ] || die "$COMMAND takes one distro"; "cmd_$COMMAND" "$1" ;;
    ssh) [ $# -ge 1 ] || die "ssh takes a distro"; cmd_ssh "$@" ;;
    run) [ $# -ge 1 ] || die "run takes one or more distros"; cmd_run "$@" ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 1 ;;
esac
