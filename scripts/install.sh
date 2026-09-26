#!/bin/bash
#
# vmm installer
#
#   curl -fsSL https://raesene.github.io/baremetalvmm/install.sh | sudo bash
#   curl -fsSL https://raesene.github.io/baremetalvmm/install.sh | sudo bash -s -- --with-services
#
# Also works from a checkout: sudo ./scripts/install.sh [options]
#
# Installs the latest (or a pinned) vmm release: the vmm and vmm-web binaries,
# helper scripts, Firecracker and the default kernels/rootfs. The release
# tarball is verified against the release's checksums.txt. Re-running the
# script is safe and upgrades in place; after the first install you can also
# use 'sudo vmm upgrade'. Running VMs are never stopped.

set -euo pipefail

# Everything runs inside main so a partially downloaded script never executes.
main() {

GITHUB_REPO="raesene/baremetalvmm"
INSTALL_DIR="/usr/local/bin"
SHARE_DIR="/usr/local/share/vmm"
DATA_DIR="/var/lib/vmm"
SYSTEMD_DIR="/etc/systemd/system"
WEB_ENV="/etc/vmm-web/environment"
FC_VERSION="v1.16.0"

VERSION="${VMM_VERSION:-}"
WITH_SERVICES=0
NO_IMAGES=0
BUILD_FROM_SOURCE=0

usage() {
    cat <<EOF
Usage: install.sh [options]

Options:
  --version X          Install vmm version X (default: latest release; or set VMM_VERSION)
  --with-services      Install and enable the vmm and vmm-web systemd services
  --no-images          Skip downloading kernels and rootfs images
  --build-from-source  Build vmm from this checkout instead of downloading a release
  -h, --help           Show this help
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="${2:-}"; shift ;;
        --version=*) VERSION="${1#*=}" ;;
        --with-services) WITH_SERVICES=1 ;;
        --no-images) NO_IMAGES=1 ;;
        --build-from-source) BUILD_FROM_SOURCE=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; usage >&2; exit 1 ;;
    esac
    shift
done
VERSION="${VERSION#v}"

say()  { printf '%s\n' "$*"; }
ok()   { printf '  [ok] %s\n' "$*"; }
warn() { printf '  [warn] %s\n' "$*" >&2; }
die()  { printf 'Error: %s\n' "$*" >&2; exit 1; }

say "vmm installer"
say "============="

[ "$(id -u)" -eq 0 ] || die "please run as root (sudo)"
[ -e /dev/kvm ] || warn "/dev/kvm not found: enable virtualisation (VT-x/AMD-V) in firmware before running VMs"

case "$(uname -m)" in
    x86_64) ARCH="amd64"; FC_ARCH="x86_64" ;;
    *) die "unsupported architecture $(uname -m): vmm releases, kernels and images are published for x86_64 (amd64) only" ;;
esac

for tool in tar sha256sum; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
    fetch_stdout() { curl -fsSL --retry 3 "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
    fetch_stdout() { wget -q -O - "$1"; }
else
    die "curl or wget is required"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# install_bin SRC DST: swap a binary in atomically, keeping the old one as
# DST.prev. A running vmm-web or firecracker keeps its old inode, so nothing
# has to be stopped first.
install_bin() {
    local src="$1" dst="$2" tmp
    tmp="$(dirname "$dst")/.$(basename "$dst").new.$$"
    install -m 0755 "$src" "$tmp"
    if [ -f "$dst" ]; then
        ln -f "$dst" "$dst.prev" 2>/dev/null || cp -p "$dst" "$dst.prev"
    fi
    mv -f "$tmp" "$dst"
}

installed_version() {
    [ -x "$INSTALL_DIR/vmm" ] || return 0
    "$INSTALL_DIR/vmm" version --json </dev/null 2>/dev/null | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'
}

PREV_VERSION="$(installed_version || true)"

# ---------- vmm binaries ----------

if [ "$BUILD_FROM_SOURCE" = 1 ]; then
    [ -f go.mod ] && grep -q "module github.com/$GITHUB_REPO" go.mod \
        || die "--build-from-source must be run from the root of a baremetalvmm checkout"
    command -v go >/dev/null 2>&1 || die "Go is required to build from source"
    say "Building vmm from source..."
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
    VERSION="${VERSION#v}"
    LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$TMP/vmm" ./cmd/vmm
    CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$TMP/vmm-web" ./cmd/vmm-web
    mkdir -p "$TMP/scripts"
    cp scripts/*.sh scripts/*.service "$TMP/scripts/"
else
    if [ -z "$VERSION" ]; then
        say "Finding the latest release..."
        VERSION=$(fetch_stdout "https://api.github.com/repos/$GITHUB_REPO/releases?per_page=100" \
            | grep -o '"tag_name": *"v[0-9][0-9.]*"' | head -1 | sed -E 's/.*"v([^"]+)"/\1/') || true
        [ -n "$VERSION" ] || die "could not determine the latest release (GitHub API unreachable or rate limited?); pass --version"
    fi
    TARBALL="vmm_${VERSION}_linux_${ARCH}.tar.gz"
    BASE="https://github.com/$GITHUB_REPO/releases/download/v$VERSION"

    say "Downloading vmm $VERSION..."
    fetch "$BASE/$TARBALL" "$TMP/$TARBALL" || die "download failed: $BASE/$TARBALL"
    fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || die "download failed: $BASE/checksums.txt"
    grep " $TARBALL\$" "$TMP/checksums.txt" > "$TMP/expected.sha256" || die "checksums.txt has no entry for $TARBALL"
    (cd "$TMP" && sha256sum -c --status expected.sha256) || die "checksum verification failed for $TARBALL"
    ok "checksum verified"
    tar -xzf "$TMP/$TARBALL" -C "$TMP" vmm vmm-web scripts
fi

install_bin "$TMP/vmm" "$INSTALL_DIR/vmm"
install_bin "$TMP/vmm-web" "$INSTALL_DIR/vmm-web"
ok "vmm and vmm-web $VERSION installed to $INSTALL_DIR"

# Helper scripts (kernel/rootfs builders, uninstaller) and reference units.
mkdir -p "$SHARE_DIR/systemd"
for f in "$TMP"/scripts/*.sh; do
    [ -f "$f" ] && install -m 0755 "$f" "$SHARE_DIR/$(basename "$f")"
done
for f in "$TMP"/scripts/*.service; do
    [ -f "$f" ] && install -m 0644 "$f" "$SHARE_DIR/systemd/$(basename "$f")"
done
ok "helper scripts installed to $SHARE_DIR"

mkdir -p "$DATA_DIR"/{config,vms,images/kernels,images/rootfs,mounts,sockets,logs,state}
chmod 0700 "$DATA_DIR"

# ---------- Firecracker ----------

FC_BIN="$INSTALL_DIR/firecracker"
FC_CURRENT=""
[ -x "$FC_BIN" ] && FC_CURRENT=$("$FC_BIN" --version 2>/dev/null | head -1 | awk '{print $NF}') || true
if [ "$FC_CURRENT" != "$FC_VERSION" ]; then
    say "Installing Firecracker $FC_VERSION..."
    FC_TGZ="firecracker-${FC_VERSION}-${FC_ARCH}.tgz"
    FC_BASE="https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION"
    fetch "$FC_BASE/$FC_TGZ" "$TMP/$FC_TGZ" || die "Firecracker download failed"
    fetch "$FC_BASE/$FC_TGZ.sha256.txt" "$TMP/$FC_TGZ.sha256.txt" || die "Firecracker checksum download failed"
    (cd "$TMP" && sha256sum -c --status "$FC_TGZ.sha256.txt") || die "Firecracker checksum verification failed"
    tar -xzf "$TMP/$FC_TGZ" -C "$TMP"
    install_bin "$TMP/release-${FC_VERSION}-${FC_ARCH}/firecracker-${FC_VERSION}-${FC_ARCH}" "$FC_BIN"
    if [ -n "$FC_CURRENT" ]; then
        ok "firecracker $FC_CURRENT -> $FC_VERSION (running VMs keep the old version until restarted)"
    else
        ok "firecracker $FC_VERSION installed"
    fi
else
    ok "firecracker $FC_VERSION already installed"
fi

# ---------- Images ----------

if [ "$NO_IMAGES" = 0 ]; then
    say "Downloading default images (skipped if already present)..."
    if "$INSTALL_DIR/vmm" image pull </dev/null >"$TMP/pull.log" 2>&1; then
        ok "default kernel and Ubuntu 24.04 rootfs"
    else
        warn "default images: $(tail -1 "$TMP/pull.log"); run 'sudo vmm image pull' later"
    fi
    for kernel in k8s-kernel security-kernel; do
        if "$INSTALL_DIR/vmm" kernel pull "$kernel" </dev/null >"$TMP/pull.log" 2>&1; then
            ok "$kernel"
        elif grep -q "already exists" "$TMP/pull.log"; then
            ok "$kernel already present"
        else
            warn "$kernel: $(tail -1 "$TMP/pull.log"); run 'sudo vmm kernel pull $kernel' later"
        fi
    done
fi

# ---------- systemd services ----------

WEB_PASSWORD=""
gen_password() { head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

if [ "$WITH_SERVICES" = 1 ]; then
    command -v systemctl >/dev/null 2>&1 || die "--with-services needs systemd"
    say "Installing systemd services..."
    for unit in vmm.service vmm-web.service; do
        if [ ! -f "$SYSTEMD_DIR/$unit" ]; then
            install -m 0644 "$SHARE_DIR/systemd/$unit" "$SYSTEMD_DIR/$unit"
            ok "$unit installed"
        elif ! cmp -s "$SHARE_DIR/systemd/$unit" "$SYSTEMD_DIR/$unit"; then
            say "  [note] keeping your existing $unit (differs from $SHARE_DIR/systemd/$unit)"
        fi
    done
    mkdir -p "$(dirname "$WEB_ENV")"
    # Older installers wrote a placeholder password that vmm-web accepts;
    # replace it so the web UI is never exposed with a published password.
    if [ ! -s "$WEB_ENV" ] || grep -q "please-set-a-real-password" "$WEB_ENV"; then
        WEB_PASSWORD="$(gen_password)"
        ( umask 077; printf 'VMM_WEB_PASSWORD=%s\n' "$WEB_PASSWORD" > "$WEB_ENV" )
        ok "generated a vmm-web password in $WEB_ENV"
    fi
    chmod 600 "$WEB_ENV"
    systemctl daemon-reload
    systemctl enable --quiet vmm.service
    ok "vmm.service enabled (starts VMs marked for auto-start at boot)"
    systemctl enable --quiet vmm-web.service
    if systemctl is-active --quiet vmm-web.service; then
        systemctl restart vmm-web.service && ok "vmm-web.service restarted"
    else
        systemctl start vmm-web.service && ok "vmm-web.service started"
    fi
elif command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet vmm-web.service; then
    # Upgrading an existing install: only the web UI needs a restart. vmm.service
    # is deliberately left alone, since stopping it stops every running VM.
    systemctl restart vmm-web.service && ok "vmm-web.service restarted"
fi

# ---------- Summary ----------

say ""
if [ -n "$PREV_VERSION" ] && [ "$PREV_VERSION" != "$VERSION" ]; then
    say "vmm upgraded: $PREV_VERSION -> $VERSION. Running VMs were not touched."
else
    say "vmm $VERSION is installed."
fi
say ""
if [ -z "$PREV_VERSION" ]; then
    say "Next steps:"
    say "  vmm config init                              # optional: write ~/.config/vmm/config.json"
    say "  sudo vmm create myvm --cpus 2 --memory 1024"
    say "  sudo vmm start myvm && vmm ssh myvm"
    say ""
fi
if [ -n "$WEB_PASSWORD" ]; then
    say "Web console: http://127.0.0.1:8080  (user: admin, password: $WEB_PASSWORD)"
    say "  The password is stored in $WEB_ENV. From another machine: ssh -L 8080:127.0.0.1:8080 <this-host>"
    say ""
elif [ "$WITH_SERVICES" = 0 ] && [ ! -f "$SYSTEMD_DIR/vmm-web.service" ]; then
    say "Optional: install the auto-start and web console services by re-running with --with-services"
    say ""
fi
say "Upgrade later with: sudo vmm upgrade   (check first with: vmm upgrade --check)"
}

main "$@"
