package provider

import (
	"strings"
	"testing"

	api "github.com/cleura/terraform-provider-cleura/api"
	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestWorkersListToMap verifies workers are indexed by their (immutable) name —
// the basis for the by-name reconcile in Update.
func TestWorkersListToMap(t *testing.T) {
	ws := []resource_gardener_shoot.WorkersValue{
		{Name: basetypes.NewStringValue("wg-1")},
		{Name: basetypes.NewStringValue("wg-2")},
	}
	m := WorkersListToMap(ws)
	if len(m) != 2 {
		t.Fatalf("want 2 entries, got %d", len(m))
	}
	for _, name := range []string{"wg-1", "wg-2"} {
		if _, ok := m[name]; !ok {
			t.Errorf("missing worker %q in map", name)
		}
	}
}

// TestWorkerUpdateBodyWithExplicitEmptyArrays verifies that empty (but non-nil)
// label/annotation/taint slices serialize as [] rather than being omitted, so the
// API actually clears existing values instead of leaving them in place.
func TestWorkerUpdateBodyWithExplicitEmptyArrays(t *testing.T) {
	body := api.GardenerEditShootWorker{
		Labels:      &[]api.GardenerLabel{},
		Annotations: &[]api.GardenerAnnotation{},
		Taints:      &[]api.GardenerEditShootNodeTaint{},
	}

	b, err := workerUpdateBodyWithExplicitEmptyArrays(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := string(b)
	for _, want := range []string{`"labels":[]`, `"annotations":[]`, `"taints":[]`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s in JSON output, got: %s", want, out)
		}
	}
}
