package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndCompareVersions(t *testing.T) {
	if _, ok := ParseVersion("dev"); ok {
		t.Fatal("dev should not parse")
	}
	if v, ok := ParseVersion("v1.2.3-rc1"); !ok || v.Major != 1 || v.Minor != 2 || v.Patch != 3 || v.Pre != "rc1" {
		t.Fatalf("ParseVersion(v1.2.3-rc1) = %+v, %v", v, ok)
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"0.13.1", "0.13.0", 1},
		{"0.13.0", "0.13.1", -1},
		{"0.13.1", "v0.13.1", 0},
		{"0.10.0", "0.9.9", 1}, // numeric, not lexical
		{"1.0.0", "0.99.99", 1},
		{"0.14.0-rc1", "0.14.0", -1},
		{"0.14.0-rc2", "0.14.0-rc1", 1},
		{"dev", "0.1.0", -1},
		{"0.1.0", "dev", 1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	got := ParseChecksums(strings.NewReader(sum + "  vmm_0.1.0_linux_amd64.tar.gz\n" + sum + " *binary.tgz\nnot a checksum line\n"))
	if got["vmm_0.1.0_linux_amd64.tar.gz"] != sum || got["binary.tgz"] != sum || len(got) != 2 {
		t.Fatalf("ParseChecksums = %v", got)
	}
}

// ---------- fake GitHub ----------

type tarFile struct {
	name string
	body string
}

func makeTarGz(t *testing.T, files []tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fakeGitHub struct {
	*httptest.Server
	files    map[string][]byte // path -> body
	releases []map[string]any
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{files: map[string][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r/releases":
			json.NewEncoder(w).Encode(f.releases)
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/releases/tags/"):
			tag := strings.TrimPrefix(r.URL.Path, "/repos/o/r/releases/tags/")
			for _, rel := range f.releases {
				if rel["tag_name"] == tag {
					json.NewEncoder(w).Encode(rel)
					return
				}
			}
			http.NotFound(w, r)
		default:
			body, ok := f.files[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(body)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) client() *Client {
	return &Client{HTTP: f.Client(), APIBase: f.URL, GitHubURL: f.URL, Repo: "o/r"}
}

// addRelease publishes a release whose assets are served by the fake.
func (f *fakeGitHub) addRelease(tag string, prerelease bool, assets map[string][]byte) {
	var list []map[string]string
	for name, body := range assets {
		p := "/download/" + tag + "/" + name
		f.files[p] = body
		list = append(list, map[string]string{"name": name, "browser_download_url": f.URL + p})
	}
	f.releases = append(f.releases, map[string]any{
		"tag_name": tag, "prerelease": prerelease, "draft": false,
		"published_at": "2026-09-26T10:00:00Z", "html_url": "https://example/" + tag, "assets": list,
	})
}

func releaseAssets(t *testing.T, version string, files []tarFile) (map[string][]byte, string) {
	tgz := makeTarGz(t, files)
	name := TarballName(version, "amd64")
	return map[string][]byte{
		name:            tgz,
		"checksums.txt": []byte(fmt.Sprintf("%s  %s\n", sha(tgz), name)),
	}, name
}

var goodFiles = []tarFile{
	{"vmm", "vmm-binary"},
	{"vmm-web", "web-binary"},
	{"README.md", "readme"},
	{"scripts/install.sh", "#!/bin/bash\nFC_VERSION=\"v1.17.0\"\n"},
	{"scripts/vmm-web.service", "[Unit]\n"},
	{"scripts/dev-rootfs-files/nested", "skip me"},
	{"other/file", "skip me too"},
}

func TestLatestReleaseSkipsNonBinaryAndPrereleases(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRelease("kernel-6.1.100", false, nil)
	f.addRelease("v0.13.1", false, nil)
	f.addRelease("v0.14.0-rc1", true, nil)
	f.addRelease("v0.10.0", false, nil)
	f.addRelease("rootfs-24.04-20260901", false, nil)

	rel, err := f.client().LatestRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.13.1" {
		t.Fatalf("latest = %s, want 0.13.1", rel.Version)
	}
}

func TestReleaseByVersion(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRelease("v0.13.0", false, nil)
	c := f.client()
	if rel, err := c.ReleaseByVersion(context.Background(), "v0.13.0"); err != nil || rel.Tag != "v0.13.0" {
		t.Fatalf("ReleaseByVersion = %+v, %v", rel, err)
	}
	if _, err := c.ReleaseByVersion(context.Background(), "0.99.0"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing release error = %v", err)
	}
	if _, err := c.ReleaseByVersion(context.Background(), "../../x"); err == nil {
		t.Fatal("invalid version accepted")
	}
}

func TestFetchVerifiesAndExtractsOnlyKnownFiles(t *testing.T) {
	f := newFakeGitHub(t)
	assets, _ := releaseAssets(t, "0.14.0", goodFiles)
	f.addRelease("v0.14.0", false, assets)
	c := f.client()
	rel, err := c.ReleaseByVersion(context.Background(), "0.14.0")
	if err != nil {
		t.Fatal(err)
	}

	b, err := c.Fetch(context.Background(), rel, "amd64", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(b.VMM); string(data) != "vmm-binary" {
		t.Fatalf("vmm = %q", data)
	}
	if b.VMMWeb == "" || b.Scripts == "" {
		t.Fatalf("bundle = %+v", b)
	}
	if b.FirecrackerVersion != "v1.17.0" {
		t.Fatalf("FirecrackerVersion = %q", b.FirecrackerVersion)
	}
	for _, skipped := range []string{"README.md", "other/file", "scripts/dev-rootfs-files/nested"} {
		if fileExists(filepath.Join(b.Dir, skipped)) {
			t.Errorf("%s should not have been extracted", skipped)
		}
	}
}

func TestFetchRejectsBadChecksum(t *testing.T) {
	f := newFakeGitHub(t)
	assets, name := releaseAssets(t, "0.14.0", goodFiles)
	assets["checksums.txt"] = []byte(strings.Repeat("0", 64) + "  " + name + "\n")
	f.addRelease("v0.14.0", false, assets)
	c := f.client()
	rel, _ := c.ReleaseByVersion(context.Background(), "0.14.0")
	if _, err := c.Fetch(context.Background(), rel, "amd64", t.TempDir()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Fetch error = %v, want checksum mismatch", err)
	}
}

func TestFetchRequiresChecksumsAndArch(t *testing.T) {
	f := newFakeGitHub(t)
	assets, _ := releaseAssets(t, "0.14.0", goodFiles)
	delete(assets, "checksums.txt")
	f.addRelease("v0.14.0", false, assets)
	c := f.client()
	rel, _ := c.ReleaseByVersion(context.Background(), "0.14.0")
	if _, err := c.Fetch(context.Background(), rel, "amd64", t.TempDir()); err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Fatalf("missing checksums error = %v", err)
	}
	if _, err := c.Fetch(context.Background(), rel, "arm64", t.TempDir()); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("missing arch error = %v", err)
	}
}

func TestExtractRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "evil.tar.gz")
	os.WriteFile(tgz, makeTarGz(t, []tarFile{{"../vmm", "evil"}}), 0o600)
	if err := extractRelease(tgz, filepath.Join(dir, "out")); err == nil {
		t.Fatal("path traversal was not rejected")
	}
	if fileExists(filepath.Join(dir, "vmm")) {
		t.Fatal("file written outside destination")
	}
}

func TestReplaceFileKeepsBackupAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "vmm")
	os.WriteFile(dst, []byte("old"), 0o755)
	src := filepath.Join(dir, "new")
	os.WriteFile(src, []byte("new"), 0o600)

	if err := ReplaceFile(src, dst, 0o755, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "new" {
		t.Fatalf("dst = %q", data)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if data, _ := os.ReadFile(dst + PrevSuffix); string(data) != "old" {
		t.Fatalf("prev = %q", data)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".vmm.new-*")); len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}

	if err := Rollback(dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "old" {
		t.Fatalf("after rollback dst = %q", data)
	}
	if err := Rollback(dst); err == nil {
		t.Fatal("second rollback should fail: nothing left to restore")
	}
}

func TestReplaceFileWithoutExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("x"), 0o600)
	dst := filepath.Join(dir, "sub", "tool")
	if err := ReplaceFile(src, dst, 0o644, true); err != nil {
		t.Fatal(err)
	}
	if fileExists(dst + PrevSuffix) {
		t.Fatal("no backup expected for a fresh install")
	}
}

func TestInstallFirecracker(t *testing.T) {
	f := newFakeGitHub(t)
	const v = "v1.17.0"
	name := "firecracker-" + v + "-x86_64.tgz"
	tgz := makeTarGz(t, []tarFile{
		{"release-" + v + "-x86_64/jailer-" + v + "-x86_64", "jailer"},
		{"release-" + v + "-x86_64/firecracker-" + v + "-x86_64", "fc-binary"},
	})
	base := "/firecracker-microvm/firecracker/releases/download/" + v + "/"
	f.files[base+name] = tgz
	f.files[base+name+".sha256.txt"] = []byte(sha(tgz) + "  " + name + "\n")

	dst := filepath.Join(t.TempDir(), "firecracker")
	os.WriteFile(dst, []byte("old-fc"), 0o755)
	if err := f.client().InstallFirecracker(context.Background(), v, "amd64", dst, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "fc-binary" {
		t.Fatalf("firecracker = %q", data)
	}
	if data, _ := os.ReadFile(dst + PrevSuffix); string(data) != "old-fc" {
		t.Fatalf("firecracker.prev = %q", data)
	}

	// A tampered archive must not be installed.
	f.files[base+name+".sha256.txt"] = []byte(strings.Repeat("1", 64) + "  " + name + "\n")
	if err := f.client().InstallFirecracker(context.Background(), v, "amd64", dst, t.TempDir()); err == nil {
		t.Fatal("tampered Firecracker archive was installed")
	}
}

// TestRealReleases exercises the real GitHub releases (vmm tarball layout,
// checksums.txt format, Firecracker archive layout). It needs network access,
// so it only runs with VMM_NETWORK_TESTS=1.
func TestRealReleases(t *testing.T) {
	if os.Getenv("VMM_NETWORK_TESTS") == "" {
		t.Skip("set VMM_NETWORK_TESTS=1 to run against GitHub")
	}
	c := NewClient()
	ctx := context.Background()
	rel, err := c.LatestRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("latest release: %s", rel.Version)

	b, err := c.Fetch(ctx, rel, "amd64", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b.VMMWeb == "" || b.Scripts == "" || b.FirecrackerVersion == "" {
		t.Fatalf("incomplete bundle: %+v", b)
	}
	t.Logf("bundle pins Firecracker %s", b.FirecrackerVersion)

	dst := filepath.Join(t.TempDir(), "firecracker")
	if err := c.InstallFirecracker(ctx, b.FirecrackerVersion, "amd64", dst, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dst); err != nil || info.Size() < 1<<20 {
		t.Fatalf("firecracker binary looks wrong: %v %v", info, err)
	}
}
