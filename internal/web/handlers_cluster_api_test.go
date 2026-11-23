package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/sshkey"
)

func TestAPIClusterListHidesJoinToken(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	paths := cfg.GetPaths()
	if err := os.MkdirAll(paths.Clusters, 0o700); err != nil {
		t.Fatal(err)
	}

	cl := cluster.NewCluster("c1", 1, "1.37.1", "", "")
	cl.JoinToken = "abcdef.0123456789abcdef"
	cl.JoinCAHash = "sha256:deadbeef"
	if err := cl.Save(paths.Clusters); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: cfg}
	rec := httptest.NewRecorder()
	s.handleAPIClusterList(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))

	body := rec.Body.String()
	for _, secret := range []string{cl.JoinToken, cl.JoinCAHash, "join_token"} {
		if strings.Contains(body, secret) {
			t.Errorf("response contains %q: %s", secret, body)
		}
	}
	var got []cluster.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not a list of cluster.Status: %v", err)
	}
	if len(got) != 1 || got[0].Name != "c1" || len(got[0].Nodes) != 2 {
		t.Errorf("got %+v", got)
	}
}

// The API, like the web form, needs no SSH key: the managed key provisions
// the cluster. The request names a missing image so it stops before any VM
// is created.
func TestAPIClusterCreateWithoutSSHKey(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	s := &Server{cfg: cfg}

	body := strings.NewReader(`{"name":"c1","image":"no-such-image"}`)
	rec := httptest.NewRecorder()
	s.handleAPIClusterCreate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/clusters", body))

	if strings.Contains(rec.Body.String(), "SSH key") {
		t.Fatalf("create without SSH keys was refused: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no-such-image") {
		t.Errorf("expected to get as far as the image check, got %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(sshkey.PrivateKeyPath(cfg.GetPaths().SSH)); err != nil {
		t.Errorf("managed key not created for provisioning: %v", err)
	}
}
