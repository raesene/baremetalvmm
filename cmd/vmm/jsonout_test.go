package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/raesene/baremetalvmm/internal/vm"
)

// Callers parse these, so an empty list must be [] rather than null.
func TestEmptyListsAreArrays(t *testing.T) {
	for _, v := range []any{[]vm.VM{}, clusterStatuses(nil, t.TempDir())} {
		var buf bytes.Buffer
		if err := writeJSON(&buf, v); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(buf.String()); got != "[]" {
			t.Errorf("empty list printed as %q, want []", got)
		}
	}
}

func TestVMJSONFillsBackend(t *testing.T) {
	v := vm.NewVM("a")
	if got := vmJSON(v).Backend; got != "firecracker" {
		t.Errorf("backend = %q, want firecracker", got)
	}
	if v.Backend != "" {
		t.Error("vmJSON changed the VM it was given")
	}
}
