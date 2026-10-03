package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/raesene/baremetalvmm/internal/portforward"
	"github.com/raesene/baremetalvmm/internal/vm"
)

func testRows() []portForwardRow {
	return []portForwardRow{
		{Entry: portforward.Entry{VMName: "web", VMState: vm.StateRunning, GuestIP: "172.16.0.5", HostPort: 8080, GuestPort: 80, Protocol: "tcp", Status: portforward.StatusActive},
			HostAddr: "192.168.1.10:8080", ChipClass: "chip-running"},
		{Entry: portforward.Entry{VMName: "dns", VMState: vm.StateRunning, GuestIP: "172.16.0.6", HostPort: 5353, GuestPort: 53, Protocol: "udp", Status: portforward.StatusMissing},
			HostAddr: "192.168.1.10:5353", ChipClass: "chip-error"},
		{Entry: portforward.Entry{VMName: "old", VMState: vm.StateRunning, GuestIP: "172.16.0.8", HostPort: 4000, GuestPort: 80, Protocol: "tcp", Status: portforward.StatusOutdated},
			HostAddr: "192.168.1.10:4000", ChipClass: "chip-created"},
		{Entry: portforward.Entry{VMName: "batch", VMState: vm.StateStopped, GuestIP: "", HostPort: 2222, GuestPort: 22, Protocol: "tcp", Status: portforward.StatusInactive},
			HostAddr: "192.168.1.10:2222", ChipClass: "chip-stopped"},
	}
}

func render(t *testing.T, page string, data map[string]interface{}) string {
	t.Helper()
	s := &Server{}
	if err := s.loadTemplates(); err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	data["Active"] = "port-forwards"
	data["CSRFToken"] = "tok"
	var buf bytes.Buffer
	if err := s.templates[page].ExecuteTemplate(&buf, page, data); err != nil {
		t.Fatalf("execute %s: %v", page, err)
	}
	return buf.String()
}

func TestPortForwardsPageRenders(t *testing.T) {
	out := render(t, "port_forwards.html", map[string]interface{}{
		"HostIP":          "192.168.1.10",
		"PortForwards":    testRows(),
		"ForwardsMissing": true,
	})
	for _, want := range []string{
		"192.168.1.10:8080",
		`<tr data-vm="web">`,
		`href="/vms/dns"`,
		"172.16.0.6:53",
		`class="chip chip-error"`,
		`action="/vms/dns/port-forwards/apply"`, // apply offered for the missing forward
		`name="return" value="port-forwards"`,
		`href="/port-forwards" class="is-active"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("page missing %s", want)
		}
	}
	if strings.Count(out, "/port-forwards/apply") != 2 {
		t.Error("apply should be offered for the missing and outdated forwards only")
	}
	if !strings.Contains(out, `action="/vms/old/port-forwards/apply"`) {
		t.Error("apply not offered for the outdated forward")
	}
	if !strings.Contains(out, "<td class=\"data\">:22</td>") {
		t.Error("stopped VM without an IP should show just the guest port")
	}
}

func TestPortForwardsPageEmpty(t *testing.T) {
	out := render(t, "port_forwards.html", map[string]interface{}{})
	if !strings.Contains(out, "No port forwards yet") {
		t.Error("empty state not shown")
	}
}

func TestVMDetailPortForwardPanel(t *testing.T) {
	v := &vm.VM{Name: "dns", State: vm.StateRunning, IPAddress: "172.16.0.6", CreatedAt: time.Now()}
	out := render(t, "vm_detail.html", map[string]interface{}{
		"VM":              v,
		"PortForwards":    testRows()[1:2],
		"ForwardsMissing": true,
		"MaxUpload":       DefaultMaxUploadBytes,
	})
	for _, want := range []string{
		`action="/vms/dns/port-forwards"`,
		`name="guest_port" value="53"`,
		`name="protocol" value="udp"`,
		"apply now",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail page missing %s", want)
		}
	}

	out = render(t, "vm_detail.html", map[string]interface{}{
		"VM":        &vm.VM{Name: "idle", State: vm.StateStopped, CreatedAt: time.Now()},
		"MaxUpload": DefaultMaxUploadBytes,
	})
	if !strings.Contains(out, "apply when the VM starts") {
		t.Error("stopped VM with no forwards should explain when forwards apply")
	}
	if !strings.Contains(out, `action="/vms/idle/port-forwards"`) {
		t.Error("add form missing for a stopped VM")
	}
}

func TestFormForward(t *testing.T) {
	tests := []struct {
		form    string
		want    vm.PortForward
		wantErr bool
	}{
		{form: "host_port=8080&guest_port=80", want: vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}},
		{form: "host_port=5353&guest_port=53&protocol=udp", want: vm.PortForward{HostPort: 5353, GuestPort: 53, Protocol: "udp"}},
		{form: "host_port=x&guest_port=80", wantErr: true},
		{form: "host_port=8080&guest_port=80&protocol=icmp", wantErr: true},
		{form: "host_port=99999&guest_port=80", wantErr: true},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/vms/x/port-forwards", strings.NewReader(tt.form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		got, err := formForward(r)
		if (err != nil) != tt.wantErr {
			t.Errorf("formForward(%q) err = %v", tt.form, err)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("formForward(%q) = %+v, want %+v", tt.form, got, tt.want)
		}
	}
}

func TestReturnPathAllowsOnlyKnownPages(t *testing.T) {
	for value, want := range map[string]string{
		"port-forwards":        "/port-forwards",
		"":                     "/vms/web",
		"https://evil.example": "/vms/web",
		"//evil.example":       "/vms/web",
	} {
		r := httptest.NewRequest(http.MethodPost, "/vms/web/port-forwards", strings.NewReader("return="+url.QueryEscape(value)))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := returnPath(r, "web"); got != want {
			t.Errorf("returnPath(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestPortForwardsPageHidesFixNoteWhenAllActive(t *testing.T) {
	out := render(t, "port_forwards.html", map[string]interface{}{"PortForwards": testRows()[:1]})
	if strings.Contains(out, "Use <strong>apply</strong>") {
		t.Error("missing/outdated explanation shown with no forwards needing it")
	}
}
