package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/guestfile"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
)

const (
	// DefaultMaxUploadBytes caps a single file upload into a VM.
	DefaultMaxUploadBytes int64 = 4 << 30

	// maxListEntries caps how many entries the file browser shows for one
	// directory, so a huge directory can't produce a multi-megabyte page.
	maxListEntries = 1000

	// guestUser is the account files are read and written as. vmm only
	// installs its managed key for root.
	guestUser = "root"
)

// SetMaxUploadBytes sets the largest file that can be uploaded into a VM.
func (s *Server) SetMaxUploadBytes(n int64) {
	if n > 0 {
		s.maxUploadBytes = n
	}
}

// fileError carries the HTTP status for a file browser failure.
type fileError struct {
	code int
	msg  string
}

func (e *fileError) Error() string { return e.msg }

// fileErrorStatus maps an error from the guest to an HTTP status and message.
func fileErrorStatus(err error) (int, string) {
	var fe *fileError
	switch {
	case errors.As(err, &fe):
		return fe.code, fe.msg
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound, "no such file or directory"
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden, "permission denied"
	case errors.Is(err, guestfile.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, guestfile.ErrIsDir), errors.Is(err, guestfile.ErrNotRegular):
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, err.Error()
	}
}

// openGuest loads the VM named in the URL and opens an SFTP session to it.
// The caller must Close the returned client.
func (s *Server) openGuest(r *http.Request) (*vm.VM, *guestfile.Client, error) {
	name := chi.URLParam(r, "name")
	if err := validate.VMName(name); err != nil {
		return nil, nil, &fileError{http.StatusBadRequest, err.Error()}
	}
	paths := s.cfg.GetPaths()

	v, err := vm.Load(paths.VMs, name)
	if err != nil {
		return nil, nil, &fileError{http.StatusNotFound, "VM not found"}
	}
	firecracker.NewClient().UpdateVMState(v)
	if v.State != vm.StateRunning {
		return nil, nil, &fileError{http.StatusConflict, "VM is not running"}
	}
	if v.IPAddress == "" {
		return nil, nil, &fileError{http.StatusConflict, "VM has no IP address"}
	}

	auth, err := findSSHAuth(paths.SSH)
	if err != nil {
		return nil, nil, &fileError{http.StatusInternalServerError, "no SSH key available: " + err.Error()}
	}
	client, err := guestfile.Dial(v.IPAddress, guestUser, auth)
	if err != nil {
		return nil, nil, &fileError{http.StatusBadGateway, err.Error()}
	}
	return v, client, nil
}

// closeGuest ends an SFTP session opened by openGuest. A failure here only
// affects the connection being torn down, so it is logged, not returned.
func closeGuest(c *guestfile.Client) {
	if err := c.Close(); err != nil {
		log.Printf("files: closing sftp session: %v", err)
	}
}

// clearReadDeadline lifts the server-wide ReadTimeout for a transfer. Without
// it, an upload is cut off after 30 seconds, and a long download has its
// request context cancelled when the idle read deadline passes.
func clearReadDeadline(w http.ResponseWriter) {
	if err := http.NewResponseController(w).SetReadDeadline(time.Time{}); err != nil {
		log.Printf("files: could not clear read deadline: %v", err)
	}
}

// fileRow is a directory entry with the URLs the template needs. URLs are
// built here because html/template does not URL-escape hx-get attributes.
type fileRow struct {
	guestfile.Entry
	ListURL     string
	DownloadURL string
}

func filesURL(vmName, endpoint, key, value string) string {
	return "/vms/" + url.PathEscape(vmName) + endpoint + "?" + url.Values{key: {value}}.Encode()
}

// handleFilesList renders the file browser fragment for one guest directory.
// Errors are rendered into the fragment with a 200 so HTMX swaps them in.
func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	data := map[string]interface{}{
		"VMName":    name,
		"MaxUpload": s.maxUploadBytes,
	}
	render := func(err error) {
		if err != nil {
			_, msg := fileErrorStatus(err)
			data["Error"] = msg
		}
		s.renderTemplate(w, "vm_files.html", data)
	}

	_, client, err := s.openGuest(r)
	if err != nil {
		render(err)
		return
	}
	defer closeGuest(client)

	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = client.Home()
	}
	dir, err = guestfile.CleanPath(dir)
	if err != nil {
		render(&fileError{http.StatusBadRequest, err.Error()})
		return
	}
	data["Dir"] = dir
	data["Crumbs"] = crumbRows(name, dir)
	data["UploadURL"] = filesURL(name, "/files/upload", "dir", dir)
	if dir != "/" {
		data["ParentURL"] = filesURL(name, "/files", "path", path.Dir(dir))
	}

	isDir, err := client.IsDir(dir)
	if err == nil && !isDir {
		err = &fileError{http.StatusBadRequest, dir + " is not a directory"}
	}
	if err != nil {
		render(err)
		return
	}

	entries, truncated, err := client.List(dir, maxListEntries)
	if err != nil {
		render(err)
		return
	}
	rows := make([]fileRow, len(entries))
	for i, e := range entries {
		rows[i] = fileRow{Entry: e}
		if e.IsDir {
			rows[i].ListURL = filesURL(name, "/files", "path", e.Path)
		} else if e.Regular {
			rows[i].DownloadURL = filesURL(name, "/files/download", "path", e.Path)
		}
	}
	data["Entries"] = rows
	data["Truncated"] = truncated
	data["Limit"] = maxListEntries
	render(nil)
}

type crumbRow struct {
	Name string
	URL  string
}

func crumbRows(vmName, dir string) []crumbRow {
	crumbs := guestfile.Breadcrumbs(dir)
	rows := make([]crumbRow, len(crumbs))
	for i, c := range crumbs {
		rows[i] = crumbRow{Name: c.Name, URL: filesURL(vmName, "/files", "path", c.Path)}
	}
	return rows
}

// handleFileDownload streams a guest file to the browser as an attachment.
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	s.serveDownload(w, r)
}

// handleFileUpload stores the raw request body as dir/name in the guest. The
// browser sends the file itself as the body (not multipart) so it streams
// straight through to SFTP without being buffered on the host.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, err := guestfile.CleanPath(q.Get("dir"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fileName, err := guestfile.CleanName(q.Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.receiveUpload(w, r, path.Join(dir, fileName))
}

// handleAPIFilesList returns a guest directory listing as JSON.
func (s *Server) handleAPIFilesList(w http.ResponseWriter, r *http.Request) {
	_, client, err := s.openGuest(r)
	if err != nil {
		code, msg := fileErrorStatus(err)
		jsonError(w, msg, code)
		return
	}
	defer closeGuest(client)

	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = client.Home()
	}
	dir, err = guestfile.CleanPath(dir)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if isDir, err := client.IsDir(dir); err != nil || !isDir {
		if err == nil {
			err = &fileError{http.StatusBadRequest, dir + " is not a directory"}
		}
		code, msg := fileErrorStatus(err)
		jsonError(w, msg, code)
		return
	}
	entries, truncated, err := client.List(dir, maxListEntries)
	if err != nil {
		code, msg := fileErrorStatus(err)
		jsonError(w, msg, code)
		return
	}
	jsonResponse(w, map[string]interface{}{
		"path":      dir,
		"entries":   entries,
		"truncated": truncated,
	})
}

// handleAPIFileDownload returns a guest file's contents.
func (s *Server) handleAPIFileDownload(w http.ResponseWriter, r *http.Request) {
	s.serveDownload(w, r)
}

// handleAPIFileUpload stores the request body at ?path= in the guest.
func (s *Server) handleAPIFileUpload(w http.ResponseWriter, r *http.Request) {
	dest, err := guestfile.CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := guestfile.CleanName(path.Base(dest)); err != nil || dest == "/" {
		jsonError(w, "path must name a file", http.StatusBadRequest)
		return
	}
	s.receiveUpload(w, r, dest)
}

func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request) {
	fail := func(err error) {
		code, msg := fileErrorStatus(err)
		httpError(w, r, msg, code)
	}

	p, err := guestfile.CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		fail(&fileError{http.StatusBadRequest, err.Error()})
		return
	}
	clearReadDeadline(w)

	v, client, err := s.openGuest(r)
	if err != nil {
		fail(err)
		return
	}
	defer closeGuest(client)

	f, info, err := client.Open(p)
	if err != nil {
		fail(err)
		return
	}
	defer func() { _ = f.Close() }()

	// Always an attachment of opaque bytes: a guest file must never render
	// as a page on the console's origin.
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(p)})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")

	n, err := f.WriteTo(w)
	if err != nil {
		log.Printf("files: download %s:%s failed after %d bytes: %v", v.Name, p, n, err)
		return
	}
	s.sseBroker.Note(kindVM, v.Name, "download", requestSource(r), fmt.Sprintf("%s (%s)", p, humanBytes(n)))
}

func (s *Server) receiveUpload(w http.ResponseWriter, r *http.Request, dest string) {
	fail := func(err error) {
		code, msg := fileErrorStatus(err)
		httpError(w, r, msg, code)
	}

	if r.ContentLength > s.maxUploadBytes {
		fail(&fileError{http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file is larger than the %s upload limit", humanBytes(s.maxUploadBytes))})
		return
	}
	clearReadDeadline(w)

	v, client, err := s.openGuest(r)
	if err != nil {
		fail(err)
		return
	}
	defer closeGuest(client)

	start := time.Now()
	n, err := client.Upload(dest, r.Body, s.maxUploadBytes)
	if err != nil {
		if errors.Is(err, guestfile.ErrTooLarge) {
			err = &fileError{http.StatusRequestEntityTooLarge,
				fmt.Sprintf("file is larger than the %s upload limit", humanBytes(s.maxUploadBytes))}
		}
		log.Printf("files: upload %s:%s failed: %v", v.Name, dest, err)
		fail(err)
		return
	}
	log.Printf("files: uploaded %s:%s (%d bytes in %s)", v.Name, dest, n, time.Since(start).Round(time.Millisecond))
	s.sseBroker.Note(kindVM, v.Name, "upload", requestSource(r), fmt.Sprintf("%s (%s)", dest, humanBytes(n)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"path": dest, "size": n})
}

// humanBytes formats a byte count with a binary unit, e.g. "12.3 MB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
