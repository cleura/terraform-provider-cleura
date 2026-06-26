package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	api "github.com/cleura/terraform-provider-cleura/api"
	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestPreserveOrder covers the helper that keeps label/annotation/taint/worker
// lists in the user's configured order even when the API returns them reordered.
func TestPreserveOrder(t *testing.T) {
	ctx := context.Background()
	id := func(s string) string { return s } // key = the value itself (for string lists)

	desired := func(items ...string) basetypes.ListValue {
		v, d := basetypes.NewListValueFrom(ctx, basetypes.StringType{}, items)
		if d.HasError() {
			t.Fatalf("building desired list: %v", d)
		}
		return v
	}

	cases := []struct {
		name    string
		vals    []string
		desired basetypes.ListValue
		want    []string
	}{
		{"reorders to the desired order", []string{"a", "b", "c"}, desired("c", "a", "b"), []string{"c", "a", "b"}},
		{"keys absent from desired are appended in original order", []string{"a", "x", "b"}, desired("b", "a"), []string{"b", "a", "x"}},
		{"null desired is a passthrough", []string{"a", "b"}, basetypes.NewListNull(basetypes.StringType{}), []string{"a", "b"}},
		{"single element is a passthrough", []string{"only"}, desired("only"), []string{"only"}},
		// Composite keys (taints use "key\x00effect"): same base key with different
		// effects must both survive. Regression guard for F1 (taint collapse).
		{"distinct composite keys all survive", []string{"k\x00NoSchedule", "k\x00NoExecute"}, desired("k\x00NoExecute", "k\x00NoSchedule"), []string{"k\x00NoExecute", "k\x00NoSchedule"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := preserveOrder(ctx, c.vals, c.desired, id)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("preserveOrder(%v) = %v, want %v", c.vals, got, c.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestSetShootStateValues maps a hand-built API shoot into the Terraform model and
// checks the round-trip — no API call or cluster needed (a non-nil shoot is passed
// in, so the client is never used). Guards:
//   - #1/#2: hibernation schedules survive with start/end set (not a null element).
//   - #11: a failed lastOperation surfaces as a warning.
func TestSetShootStateValues(t *testing.T) {
	ctx := context.Background()

	shoot := &api.GardenerShootShoot{
		CloudProfileName: "cleuracloud",
		Kubernetes:       api.GardenerShootKubernetes{Version: "1.35.6"},
		Maintenance: api.GardenerShootMaintenance{
			AutoUpdate: api.GardenerShootAutoUpdate{KubernetesVersion: true, MachineImageVersion: true},
			TimeWindow: api.GardenerShootTimeWindow{Begin: "020000+0000", End: "060000+0000"},
		},
		ShootProvider: api.GardenerShootProvider{
			ControlPlaneConfig: api.GardenerShootControlPlaneConfig{LoadBalancerProvider: "amphora"},
			InfrastructureConfig: api.GardenerShootInfrastructureConfig{
				FloatingPoolName: "ext-net",
				Networks:         api.GardenerShootNetworks{Workers: "10.250.0.0/16"},
			},
			// no workers — keeps the fixture small; worker mapping is covered elsewhere
		},
		Hibernation: &api.GardenerShootHibernation{
			Schedules: []api.GardenerShootHibernationSchedule{
				{Start: strPtr("0 20 * * 1-5"), End: strPtr("0 8 * * 1-5")},
			},
		},
		LastOperation: &api.GardenerShootLastOperation{
			State:       api.GardenerShootLastOperationStateFailed,
			Description: "Quota exceeded for cores",
			Progress:    88,
		},
	}

	data := &resource_gardener_shoot.GardenerShootModel{}
	var diags diag.Diagnostics
	SetShootStateValues(ctx, nil, shoot, data, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected errors: %v", diags.Errors())
	}

	// #1/#2: hibernation must round-trip with start/end populated, not as a null object.
	if data.HibernationSchedules.IsNull() {
		t.Fatal("hibernation_schedules should not be null")
	}
	var scheds []resource_gardener_shoot.HibernationSchedulesValue
	if d := data.HibernationSchedules.ElementsAs(ctx, &scheds, false); d.HasError() {
		t.Fatalf("reading hibernation schedules: %v", d)
	}
	if len(scheds) != 1 {
		t.Fatalf("want 1 hibernation schedule, got %d", len(scheds))
	}
	if got := scheds[0].Start.ValueString(); got != "0 20 * * 1-5" {
		t.Errorf("hibernation start = %q, want %q (empty means the #1 null-element bug is back)", got, "0 20 * * 1-5")
	}

	// #11: a failed reconcile must surface as a warning.
	foundWarning := false
	for _, d := range diags.Warnings() {
		if strings.Contains(d.Summary(), "reconciliation has not succeeded") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected a failed-reconcile warning, got: %v", diags.Warnings())
	}
}
