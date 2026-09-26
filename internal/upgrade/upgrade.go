// Package upgrade finds, verifies and installs vmm releases from GitHub.
//
// A release is the GoReleaser tarball vmm_<version>_linux_<arch>.tar.gz
// (containing vmm, vmm-web and scripts/) plus checksums.txt. Binaries are
// replaced by renaming a verified temp file over them, so a running vmm-web
// or Firecracker keeps its old inode and nothing has to be stopped first.
// The previous binary is kept alongside as <name>.prev for rollback.
package upgrade

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultRepo      = "raesene/baremetalvmm"
	DefaultAPIBase   = "https://api.github.com"
	FirecrackerRepo  = "firecracker-microvm/firecracker"
	DefaultGitHubURL = "https://github.com"

	// PrevSuffix is appended to a replaced binary so it can be rolled back.
	PrevSuffix = ".prev"

	maxDownloadBytes = 512 << 20
	maxMetadataBytes = 8 << 20
)

// Release is a vmm binary release (a v* tag) on GitHub.
type Release struct {
	Tag         string
	Version     string // tag without the leading "v"
	Prerelease  bool
	PublishedAt time.Time
	URL         string            // release page
	Assets      map[string]string // asset name -> download URL
}

// Client talks to GitHub. The base URLs are fields so tests can point them at
// an httptest server.
type Client struct {
	HTTP      *http.Client
	APIBase   string
	GitHubURL string
	Repo      string
}

func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		APIBase:   DefaultAPIBase,
		GitHubURL: DefaultGitHubURL,
		Repo:      DefaultRepo,
	}
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r ghRelease) toRelease() *Release {
	rel := &Release{
		Tag:         r.TagName,
		Version:     strings.TrimPrefix(r.TagName, "v"),
		Prerelease:  r.Prerelease,
		PublishedAt: r.PublishedAt,
		URL:         r.HTMLURL,
		Assets:      make(map[string]string, len(r.Assets)),
	}
	for _, a := range r.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel
}

func (c *Client) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes)).Decode(v)
}

var errNotFound = errors.New("not found")

// LatestRelease returns the newest published, non-prerelease v* release. The
// repository also publishes kernel-* and rootfs-* releases, which are skipped.
func (c *Client) LatestRelease(ctx context.Context) (*Release, error) {
	var releases []ghRelease
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=100", c.APIBase, c.Repo)
	if err := c.getJSON(ctx, url, &releases); err != nil {
		return nil, fmt.Errorf("listing releases: %w", err)
	}
	var best *Release
	for _, r := range releases {
		if r.Draft || r.Prerelease || !strings.HasPrefix(r.TagName, "v") {
			continue
		}
		rel := r.toRelease()
		if _, ok := ParseVersion(rel.Version); !ok {
			continue
		}
		if best == nil || Compare(rel.Version, best.Version) > 0 {
			best = rel
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no vmm releases found in %s", c.Repo)
	}
	return best, nil
}

// ReleaseByVersion returns the release tagged v<version>.
func (c *Client) ReleaseByVersion(ctx context.Context, version string) (*Release, error) {
	version = strings.TrimPrefix(version, "v")
	if _, ok := ParseVersion(version); !ok {
		return nil, fmt.Errorf("invalid version %q (expected e.g. 0.13.1)", version)
	}
	var r ghRelease
	url := fmt.Sprintf("%s/repos/%s/releases/tags/v%s", c.APIBase, c.Repo, version)
	if err := c.getJSON(ctx, url, &r); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("release v%s not found", version)
		}
		return nil, fmt.Errorf("fetching release v%s: %w", version, err)
	}
	return r.toRelease(), nil
}

// ---------- Versions ----------

// Version is a parsed major.minor.patch version with an optional pre-release.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// ParseVersion parses "0.13.1", "v0.13.1" or "0.14.0-rc1". Development builds
// ("dev") do not parse.
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, false
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Pre = m[4]
	return v, true
}

// Compare returns -1, 0 or 1 comparing versions a and b. Unparseable versions
// sort before everything else. A pre-release sorts before its final release.
func Compare(a, b string) int {
	va, okA := ParseVersion(a)
	vb, okB := ParseVersion(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for _, d := range []int{va.Major - vb.Major, va.Minor - vb.Minor, va.Patch - vb.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	switch {
	case va.Pre == vb.Pre:
		return 0
	case va.Pre == "":
		return 1
	case vb.Pre == "":
		return -1
	case va.Pre < vb.Pre:
		return -1
	default:
		return 1
	}
}

// ---------- Downloads ----------

// Arch returns the GoReleaser architecture name for this machine.
func Arch() string { return runtime.GOARCH }

// TarballName is the release asset name for a version and architecture.
func TarballName(version, arch string) string {
	return fmt.Sprintf("vmm_%s_linux_%s.tar.gz", version, arch)
}

func (c *Client) download(ctx context.Context, url string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("GET %s: response larger than %d bytes", url, limit)
	}
	return nil
}

// downloadVerified downloads url to dest and checks its SHA256.
func (c *Client) downloadVerified(ctx context.Context, url, dest, wantSHA string) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	err = c.download(ctx, url, io.MultiWriter(f, h), maxDownloadBytes)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", filepath.Base(dest), got, wantSHA)
	}
	return nil
}

// ParseChecksums reads "<sha256>  <filename>" lines (sha256sum format).
func ParseChecksums(r io.Reader) map[string]string {
	sums := make(map[string]string)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && len(fields[0]) == 64 {
			sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
		}
	}
	return sums
}

func (c *Client) fetchChecksums(ctx context.Context, url string) (map[string]string, error) {
	var sb strings.Builder
	if err := c.download(ctx, url, &sb, maxMetadataBytes); err != nil {
		return nil, err
	}
	return ParseChecksums(strings.NewReader(sb.String())), nil
}

// Bundle is a verified, extracted release.
type Bundle struct {
	Version string
	Dir     string
	VMM     string // path to the extracted vmm binary
	VMMWeb  string // path to vmm-web, or "" if the release has none
	Scripts string // path to the extracted scripts/ directory, or ""

	// FirecrackerVersion is the Firecracker version the release's installer
	// pins (FC_VERSION in scripts/install.sh), e.g. "v1.16.0", or "".
	FirecrackerVersion string
}

// Fetch downloads the release tarball for arch into dir, verifies it against
// checksums.txt and extracts it.
func (c *Client) Fetch(ctx context.Context, rel *Release, arch, dir string) (*Bundle, error) {
	name := TarballName(rel.Version, arch)
	tarURL, ok := rel.Assets[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no %s asset (unsupported architecture?)", rel.Tag, name)
	}
	sumURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return nil, fmt.Errorf("release %s has no checksums.txt; refusing to install unverified binaries", rel.Tag)
	}
	sums, err := c.fetchChecksums(ctx, sumURL)
	if err != nil {
		return nil, fmt.Errorf("downloading checksums: %w", err)
	}
	want, ok := sums[name]
	if !ok {
		return nil, fmt.Errorf("checksums.txt has no entry for %s", name)
	}

	tarPath := filepath.Join(dir, name)
	if err := c.downloadVerified(ctx, tarURL, tarPath, want); err != nil {
		return nil, err
	}

	b := &Bundle{Version: rel.Version, Dir: filepath.Join(dir, "release")}
	if err := extractRelease(tarPath, b.Dir); err != nil {
		return nil, fmt.Errorf("extracting %s: %w", name, err)
	}
	b.VMM = filepath.Join(b.Dir, "vmm")
	if _, err := os.Stat(b.VMM); err != nil {
		return nil, fmt.Errorf("release %s does not contain a vmm binary", rel.Tag)
	}
	if p := filepath.Join(b.Dir, "vmm-web"); fileExists(p) {
		b.VMMWeb = p
	}
	if p := filepath.Join(b.Dir, "scripts"); fileExists(p) {
		b.Scripts = p
		b.FirecrackerVersion = firecrackerVersionFromInstaller(filepath.Join(p, "install.sh"))
	}
	return b, nil
}

// extractRelease extracts only the files vmm needs (vmm, vmm-web and
// scripts/*) and refuses anything that could escape dest.
func extractRelease(tarPath, dest string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return fmt.Errorf("unsafe path %q in archive", hdr.Name)
		}
		keep := name == "vmm" || name == "vmm-web" ||
			(path.Dir(name) == "scripts" && !strings.HasPrefix(path.Base(name), "."))
		if !keep {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxDownloadBytes))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

var fcVersionRe = regexp.MustCompile(`(?m)^FC_VERSION="(v\d+\.\d+\.\d+)"`)

func firecrackerVersionFromInstaller(installSh string) string {
	data, err := os.ReadFile(installSh)
	if err != nil {
		return ""
	}
	if m := fcVersionRe.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---------- Installing files ----------

// ReplaceFile installs src at dst with the given mode without ever leaving dst
// missing: it writes a temp file next to dst, keeps the current dst as
// dst.prev (when backup is set), then renames the temp file over dst. A
// running process that has dst open or executing is unaffected.
func ReplaceFile(src, dst string, mode os.FileMode, backup bool) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".new-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, in)
	in.Close()
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	if backup && fileExists(dst) {
		prev := dst + PrevSuffix
		os.Remove(prev)
		if err := os.Link(dst, prev); err != nil {
			if err := copyFile(dst, prev); err != nil {
				return fmt.Errorf("backing up %s: %w", dst, err)
			}
		}
	}
	return os.Rename(tmpName, dst)
}

// Rollback restores dst from dst.prev. The current binary is discarded.
func Rollback(dst string) error {
	prev := dst + PrevSuffix
	if !fileExists(prev) {
		return fmt.Errorf("no previous version of %s to roll back to", dst)
	}
	return os.Rename(prev, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---------- Firecracker ----------

// firecrackerArch maps a Go architecture to Firecracker's release naming.
func firecrackerArch(arch string) string {
	switch arch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return arch
}

// InstallFirecracker downloads a Firecracker release, verifies it against the
// published .sha256.txt and installs the binary at dst (keeping dst.prev).
// VMs that are already running keep using the binary they started with.
func (c *Client) InstallFirecracker(ctx context.Context, version, arch, dst, workDir string) error {
	fa := firecrackerArch(arch)
	name := fmt.Sprintf("firecracker-%s-%s.tgz", version, fa)
	base := fmt.Sprintf("%s/%s/releases/download/%s/", c.GitHubURL, FirecrackerRepo, version)

	sums, err := c.fetchChecksums(ctx, base+name+".sha256.txt")
	if err != nil {
		return fmt.Errorf("downloading Firecracker checksum: %w", err)
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("no checksum published for %s", name)
	}
	tgz := filepath.Join(workDir, name)
	if err := c.downloadVerified(ctx, base+name, tgz, want); err != nil {
		return err
	}

	member := fmt.Sprintf("release-%s-%s/firecracker-%s-%s", version, fa, version, fa)
	bin := filepath.Join(workDir, "firecracker")
	if err := extractMember(tgz, member, bin); err != nil {
		return fmt.Errorf("extracting Firecracker: %w", err)
	}
	return ReplaceFile(bin, dst, 0o755, true)
}

func extractMember(tarGz, member, dest string) error {
	f, err := os.Open(tarGz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in archive", member)
		}
		if err != nil {
			return err
		}
		if path.Clean(strings.TrimPrefix(hdr.Name, "./")) != member || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxDownloadBytes))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
}
