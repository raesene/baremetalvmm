// Package guestfile reads and writes files inside a running VM over SFTP.
//
// It is used by the web UI's file browser. Paths are always absolute guest
// paths; the guest user is whoever the SSH connection authenticated as.
package guestfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// DialTimeout bounds how long connecting to a VM's SSH server may take.
const DialTimeout = 10 * time.Second

// Entry is one item in a guest directory listing.
type Entry struct {
	Name    string      `json:"name"`
	Path    string      `json:"path"`
	Size    int64       `json:"size"`
	Mode    fs.FileMode `json:"-"`
	ModeStr string      `json:"mode"`
	ModTime time.Time   `json:"mod_time"`
	IsDir   bool        `json:"is_dir"`
	IsLink  bool        `json:"is_link"`
	// Regular is true for files that can be downloaded. Devices, sockets and
	// FIFOs are listed but not offered for download.
	Regular bool `json:"regular"`
}

// Client is an SFTP session to one VM.
type Client struct {
	sftp *sftp.Client
	conn io.Closer // underlying SSH connection; nil in tests
}

// Dial opens an SFTP session to the guest at ip as user.
func Dial(ip, user string, auth []ssh.AuthMethod) (*Client, error) {
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: auth,
		// VM host keys change every time a VM is recreated with a reused IP,
		// and the guest network is a private bridge on this host.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         DialTimeout,
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(ip, "22"), cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh connection failed: %w", err)
	}
	sc, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp session failed (is sftp-server installed in the guest?): %w", err)
	}
	return &Client{sftp: sc, conn: conn}, nil
}

// Close ends the SFTP session and the SSH connection under it.
func (c *Client) Close() error {
	err := c.sftp.Close()
	if c.conn != nil {
		if cerr := c.conn.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

// Home returns the guest user's working directory, used as the default
// directory when none is given.
func (c *Client) Home() string {
	if wd, err := c.sftp.Getwd(); err == nil && path.IsAbs(wd) {
		return path.Clean(wd)
	}
	return "/"
}

// List returns the entries in dir: directories first, then by name. At most
// limit entries are returned (0 means no limit); truncated reports whether
// any were left out.
func (c *Client) List(dir string, limit int) (entries []Entry, truncated bool, err error) {
	infos, err := c.sftp.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}

	entries = make([]Entry, 0, len(infos))
	for _, info := range infos {
		e := Entry{
			Name:    info.Name(),
			Path:    path.Join(dir, info.Name()),
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
			IsLink:  info.Mode()&fs.ModeSymlink != 0,
			Regular: info.Mode().IsRegular(),
		}
		// Resolve symlinks so links to directories can be browsed and links
		// to files downloaded. Dangling links stay as plain links.
		if e.IsLink {
			if target, err := c.sftp.Stat(e.Path); err == nil {
				e.IsDir = target.IsDir()
				e.Regular = target.Mode().IsRegular()
				e.Size = target.Size()
			}
		}
		// Go renders symlinks as "L"; show them the way ls does.
		e.ModeStr = strings.Replace(e.Mode.String(), "L", "l", 1)
		entries = append(entries, e)
	}

	sortEntries(entries)
	if limit > 0 && len(entries) > limit {
		return entries[:limit], true, nil
	}
	return entries, false, nil
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
}

// IsDir reports whether p is a directory (following symlinks).
func (c *Client) IsDir(p string) (bool, error) {
	info, err := c.sftp.Stat(p)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// Open opens a regular file for download. It refuses directories and special
// files, since reading something like /dev/zero would never finish.
func (c *Client) Open(p string) (*sftp.File, fs.FileInfo, error) {
	info, err := c.sftp.Stat(p)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, fmt.Errorf("%s %w", p, ErrIsDir)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s %w", p, ErrNotRegular)
	}
	f, err := c.sftp.Open(p)
	if err != nil {
		return nil, nil, err
	}
	return f, info, nil
}

var (
	// ErrTooLarge is returned by Upload when the data exceeds the size limit.
	ErrTooLarge = errors.New("upload exceeds the size limit")
	// ErrIsDir is returned when a file operation names a directory.
	ErrIsDir = errors.New("is a directory")
	// ErrNotRegular is returned when downloading a device, socket or FIFO.
	ErrNotRegular = errors.New("is not a regular file")
)

// Upload writes r to dest, replacing any existing file. Data goes to a
// temporary file in the same directory first and is renamed into place, so
// a failed or oversized upload never leaves a truncated file behind. An
// existing file's permissions are kept. It returns the bytes written.
func (c *Client) Upload(dest string, r io.Reader, maxBytes int64) (int64, error) {
	dir, name := path.Split(dest)

	var mode fs.FileMode = 0o644
	if info, err := c.sftp.Stat(dest); err == nil {
		if info.IsDir() {
			return 0, fmt.Errorf("%s %w", dest, ErrIsDir)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return 0, err
	}
	tmp := path.Join(dir, "."+name+".vmm-upload-"+hex.EncodeToString(suffix))

	f, err := c.sftp.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			// Best effort: the upload error is what the caller needs.
			_ = c.sftp.Remove(tmp)
		}
	}()

	// One byte over the limit tells an oversized upload apart from one that
	// is exactly maxBytes. Bounding the reader also lets sftp write
	// concurrently, which matters for throughput.
	n, err := f.ReadFrom(&io.LimitedReader{R: r, N: maxBytes + 1})
	if err == nil && n > maxBytes {
		err = ErrTooLarge
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}

	if err := c.sftp.Chmod(tmp, mode); err != nil {
		return 0, err
	}
	if err := c.rename(tmp, dest); err != nil {
		return 0, err
	}
	committed = true
	return n, nil
}

// rename replaces dest atomically where the server supports it. Plain SFTP
// rename fails if dest exists, so fall back to remove-then-rename.
func (c *Client) rename(from, to string) error {
	if err := c.sftp.PosixRename(from, to); err == nil {
		return nil
	}
	if err := c.sftp.Rename(from, to); err == nil {
		return nil
	}
	if err := c.sftp.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return c.sftp.Rename(from, to)
}

// CleanPath validates a guest path from a request: it must be absolute and
// contain no NUL bytes. The result is lexically cleaned.
func CleanPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains a NUL byte")
	}
	if !path.IsAbs(p) {
		return "", fmt.Errorf("path must be absolute: %q", p)
	}
	return path.Clean(p), nil
}

// CleanName validates a file name for an upload: a single path element that
// is not "." or "..".
func CleanName(name string) (string, error) {
	switch {
	case name == "":
		return "", errors.New("file name is required")
	case name == "." || name == "..":
		return "", fmt.Errorf("invalid file name %q", name)
	case strings.ContainsAny(name, "/\x00"):
		return "", fmt.Errorf("file name must not contain '/' or NUL: %q", name)
	case len(name) > 255:
		return "", errors.New("file name is longer than 255 bytes")
	}
	return name, nil
}

// Crumb is one segment of a breadcrumb trail for a guest path.
type Crumb struct {
	Name string
	Path string
}

// Breadcrumbs splits an absolute path into its ancestors, starting at "/".
func Breadcrumbs(p string) []Crumb {
	crumbs := []Crumb{{Name: "/", Path: "/"}}
	cur := "/"
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		crumbs = append(crumbs, Crumb{Name: part, Path: cur})
	}
	return crumbs
}
