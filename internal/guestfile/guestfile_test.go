package guestfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// newTestClient connects a Client to an in-memory SFTP server.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()

	server := sftp.NewRequestServer(struct {
		io.Reader
		io.WriteCloser
	}{sr, sw}, sftp.InMemHandler())
	go func() {
		// Closing the server's write side once it stops lets the client's
		// Close, which waits for its read loop to end, return.
		server.Serve()
		sw.Close()
	}()

	sc, err := sftp.NewClientPipe(cr, cw, sftp.UseConcurrentWrites(true))
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	c := &Client{sftp: sc}
	t.Cleanup(func() { c.Close() })
	return c
}

func writeFile(t *testing.T, c *Client, p, data string) {
	t.Helper()
	f, err := c.sftp.Create(p)
	if err != nil {
		t.Fatalf("create %s: %v", p, err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	f.Close()
}

func readFile(t *testing.T, c *Client, p string) string {
	t.Helper()
	f, _, err := c.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return buf.String()
}

func TestListSortsDirectoriesFirst(t *testing.T) {
	c := newTestClient(t)
	if err := c.sftp.Mkdir("/work"); err != nil {
		t.Fatal(err)
	}
	if err := c.sftp.Mkdir("/work/zdir"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, c, "/work/b.txt", "bb")
	writeFile(t, c, "/work/a.txt", "a")

	entries, truncated, err := c.List("/work", 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "zdir,a.txt,b.txt" {
		t.Errorf("order = %s, want zdir,a.txt,b.txt", got)
	}
	if !entries[0].IsDir || entries[0].Path != "/work/zdir" {
		t.Errorf("first entry = %+v, want directory /work/zdir", entries[0])
	}
	if !entries[2].Regular || entries[2].Size != 2 {
		t.Errorf("b.txt = %+v, want regular file of size 2", entries[2])
	}

	limited, truncated, err := c.List("/work", 2)
	if err != nil || len(limited) != 2 || !truncated {
		t.Errorf("List limit 2 = %d entries, truncated=%v, err=%v", len(limited), truncated, err)
	}
}

func TestUploadCreatesAndReplaces(t *testing.T) {
	c := newTestClient(t)

	n, err := c.Upload("/new.txt", strings.NewReader("first"), 1024)
	if err != nil || n != 5 {
		t.Fatalf("Upload new = %d, %v", n, err)
	}
	if got := readFile(t, c, "/new.txt"); got != "first" {
		t.Errorf("content = %q, want first", got)
	}

	if _, err := c.Upload("/new.txt", strings.NewReader("second"), 1024); err != nil {
		t.Fatalf("Upload replace: %v", err)
	}
	if got := readFile(t, c, "/new.txt"); got != "second" {
		t.Errorf("content after replace = %q, want second", got)
	}
	assertNoTempFiles(t, c, "/")
}

func TestUploadSizeLimit(t *testing.T) {
	c := newTestClient(t)
	writeFile(t, c, "/keep.txt", "original")

	// Exactly at the limit is allowed.
	if _, err := c.Upload("/exact.bin", strings.NewReader("12345"), 5); err != nil {
		t.Fatalf("Upload at limit: %v", err)
	}

	// Over the limit fails and leaves the existing file untouched.
	_, err := c.Upload("/keep.txt", strings.NewReader("123456"), 5)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Upload over limit err = %v, want ErrTooLarge", err)
	}
	if got := readFile(t, c, "/keep.txt"); got != "original" {
		t.Errorf("existing file changed to %q after failed upload", got)
	}
	assertNoTempFiles(t, c, "/")
}

func TestUploadOntoDirectoryFails(t *testing.T) {
	c := newTestClient(t)
	if err := c.sftp.Mkdir("/d"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload("/d", strings.NewReader("x"), 10); !errors.Is(err, ErrIsDir) {
		t.Fatalf("Upload onto a directory err = %v, want ErrIsDir", err)
	}
}

func TestOpenRejectsDirectories(t *testing.T) {
	c := newTestClient(t)
	if err := c.sftp.Mkdir("/d"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Open("/d"); !errors.Is(err, ErrIsDir) {
		t.Fatalf("Open on a directory err = %v, want ErrIsDir", err)
	}
	if _, _, err := c.Open("/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open missing err = %v, want not-exist", err)
	}
}

func assertNoTempFiles(t *testing.T, c *Client, dir string) {
	t.Helper()
	entries, _, err := c.List(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name, ".vmm-upload-") {
			t.Errorf("temporary file left behind: %s", e.Path)
		}
	}
}

func TestCleanPath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "/root", want: "/root"},
		{in: "/root/../etc//hosts/", want: "/etc/hosts"},
		{in: "/../..", want: "/"},
		{in: "/", want: "/"},
		{in: "", wantErr: true},
		{in: "root", wantErr: true},
		{in: "./x", wantErr: true},
		{in: "/a\x00b", wantErr: true},
	}
	for _, tt := range tests {
		got, err := CleanPath(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCleanName(t *testing.T) {
	for _, ok := range []string{"a.txt", ".bashrc", "name with spaces", "ü.bin", "a:b"} {
		if _, err := CleanName(ok); err != nil {
			t.Errorf("CleanName(%q) unexpected error: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "a\x00", strings.Repeat("x", 256)} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("CleanName(%q) accepted", bad)
		}
	}
}

func TestBreadcrumbs(t *testing.T) {
	got := Breadcrumbs("/var/log/nginx")
	want := []Crumb{{"/", "/"}, {"var", "/var"}, {"log", "/var/log"}, {"nginx", "/var/log/nginx"}}
	if len(got) != len(want) {
		t.Fatalf("Breadcrumbs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("crumb %d = %v, want %v", i, got[i], want[i])
		}
	}
	if root := Breadcrumbs("/"); len(root) != 1 || root[0].Path != "/" {
		t.Errorf("Breadcrumbs(/) = %v", root)
	}
}
