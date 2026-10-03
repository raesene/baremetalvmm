package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raesene/baremetalvmm/internal/guestfile"
)

// readTracker fails the test if anything reads the request body.
type readTracker struct{ read bool }

func (r *readTracker) Read(p []byte) (int, error) {
	r.read = true
	return 0, io.EOF
}

// An upload sends its CSRF token in a header. The middleware must accept it
// without parsing the form, which would consume the whole upload body.
func TestCSRFHeaderDoesNotReadBody(t *testing.T) {
	s := &Server{sessions: newSessionStore()}
	session := s.sessions.create()

	reached := false
	h := s.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	body := &readTracker{}
	req := httptest.NewRequest(http.MethodPost, "/vms/x/files/upload?dir=/root&name=a", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", s.sessions.csrfToken(session))
	req.AddCookie(&http.Cookie{Name: "vmm_session", Value: session})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !reached {
		t.Fatal("request with a valid X-CSRF-Token header was rejected")
	}
	if body.read {
		t.Error("CSRF middleware read the request body despite a header token")
	}
}

func TestCSRFFormTokenStillAccepted(t *testing.T) {
	s := &Server{sessions: newSessionStore()}
	session := s.sessions.create()

	reached := false
	h := s.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	form := "csrf_token=" + s.sessions.csrfToken(session)
	req := httptest.NewRequest(http.MethodPost, "/vms/x/stop", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "vmm_session", Value: session})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !reached {
		t.Fatal("request with a valid form csrf_token was rejected")
	}
}

func TestFileErrorStatus(t *testing.T) {
	tests := []struct {
		err  error
		code int
	}{
		{&fileError{http.StatusConflict, "VM is not running"}, http.StatusConflict},
		{fmt.Errorf("stat: %w", fs.ErrNotExist), http.StatusNotFound},
		{fmt.Errorf("open: %w", fs.ErrPermission), http.StatusForbidden},
		{guestfile.ErrTooLarge, http.StatusRequestEntityTooLarge},
		{fmt.Errorf("/root %w", guestfile.ErrIsDir), http.StatusBadRequest},
		{fmt.Errorf("/dev/zero %w", guestfile.ErrNotRegular), http.StatusBadRequest},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		if code, _ := fileErrorStatus(tt.err); code != tt.code {
			t.Errorf("fileErrorStatus(%v) = %d, want %d", tt.err, code, tt.code)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	tests := map[int64]string{
		0:             "0 B",
		1023:          "1023 B",
		1024:          "1.0 KB",
		1536:          "1.5 KB",
		5 << 20:       "5.0 MB",
		4 << 30:       "4.0 GB",
		3 << 40 / 2:   "1.5 TB",
		(1 << 50) * 2: "2.0 PB",
	}
	for n, want := range tests {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// Paths with spaces, ampersands and quotes must survive into hx-get and href
// attributes as correctly escaped query values.
func TestFilesFragmentEscapesPaths(t *testing.T) {
	s := &Server{}
	if err := s.loadTemplates(); err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}

	dir := `/root/a b&c"d`
	entries := []guestfile.Entry{
		{Name: "sub dir", Path: dir + "/sub dir", IsDir: true, ModeStr: "drwxr-xr-x"},
		{Name: "x&y.txt", Path: dir + "/x&y.txt", Regular: true, Size: 2048, ModeStr: "-rw-r--r--"},
	}
	rows := []fileRow{
		{Entry: entries[0], ListURL: filesURL("vm1", "/files", "path", entries[0].Path)},
		{Entry: entries[1], DownloadURL: filesURL("vm1", "/files/download", "path", entries[1].Path)},
	}

	var buf bytes.Buffer
	err := s.templates["vm_files.html"].ExecuteTemplate(&buf, "vm_files.html", map[string]interface{}{
		"VMName":    "vm1",
		"Dir":       dir,
		"Crumbs":    crumbRows("vm1", dir),
		"UploadURL": filesURL("vm1", "/files/upload", "dir", dir),
		"ParentURL": filesURL("vm1", "/files", "path", "/root"),
		"Entries":   rows,
		"MaxUpload": DefaultMaxUploadBytes,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		// Query values are percent-encoded; '&' between params becomes &amp;
		// only in HTML, which the browser decodes back.
		`hx-get="/vms/vm1/files?path=%2Froot%2Fa&#43;b%26c%22d%2Fsub&#43;dir"`,
		`href="/vms/vm1/files/download?path=%2Froot%2Fa&#43;b%26c%22d%2Fx%26y.txt"`,
		`data-upload-url="/vms/vm1/files/upload?dir=%2Froot%2Fa&#43;b%26c%22d"`,
		`data-files-dir="/root/a b&amp;c&#34;d"`,
		"2.0 KB",
		"sub dir/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fragment missing %s", want)
		}
	}
}

func TestFilesFragmentShowsError(t *testing.T) {
	s := &Server{}
	if err := s.loadTemplates(); err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	var buf bytes.Buffer
	err := s.templates["vm_files.html"].ExecuteTemplate(&buf, "vm_files.html", map[string]interface{}{
		"VMName": "vm1",
		"Error":  "VM is not running",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "VM is not running") {
		t.Error("error message not rendered")
	}
}
