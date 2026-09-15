package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
)

func kubeconfigModel(expiresAt, lastApplied string, expirationSeconds, renewBefore int64) *shootKubeconfigResourceModel {
	return &shootKubeconfigResourceModel{
		ExpiresAt:                types.StringValue(expiresAt),
		LastApplied:              types.StringValue(lastApplied),
		ExpirationSeconds:        types.Int64Value(expirationSeconds),
		RenewBeforeExpirySeconds: types.Int64Value(renewBefore),
	}
}

// TestKubeconfigExpiryPrefersTheAPIsAnswer pins the workaround removal: while
// the API did not report expiry, the provider estimated it from the issue time
// plus the requested lifetime. Now that expires_at is returned, it wins.
func TestKubeconfigExpiryPrefersTheAPIsAnswer(t *testing.T) {
	issued := time.Now().UTC().Add(-time.Hour)
	// The API granted less than was asked for — exactly the case the estimate
	// used to get wrong.
	granted := issued.Add(30 * time.Minute)

	got, attr, ok := kubeconfigExpiry(kubeconfigModel(
		granted.Format(time.RFC3339), issued.Format(time.RFC3339), 7200, 0))
	if !ok {
		t.Fatal("expected an expiry")
	}
	if attr != "expires_at" {
		t.Errorf("expiry came from %q, want expires_at", attr)
	}
	if !got.Equal(granted.Truncate(time.Second)) {
		t.Errorf("expiry = %s, want the API's %s", got.Format(time.RFC3339), granted.Format(time.RFC3339))
	}
}

// TestKubeconfigExpiryFallsBackForOlderState is the upgrade guard: a resource
// created before expires_at existed holds only last_applied, and must keep
// using the old estimate rather than being treated as expired.
func TestKubeconfigExpiryFallsBackForOlderState(t *testing.T) {
	issued := time.Now().UTC().Add(-time.Minute)
	got, attr, ok := kubeconfigExpiry(kubeconfigModel("", issued.Format(time.RFC3339), 3600, 0))
	if !ok {
		t.Fatal("expected an expiry from last_applied")
	}
	if attr != "last_applied" {
		t.Errorf("expiry came from %q, want last_applied", attr)
	}
	want := issued.Add(time.Hour)
	if !got.Equal(want.Truncate(time.Second)) {
		t.Errorf("expiry = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	// The whole point: it must not already be due for renewal.
	if time.Now().After(got) {
		t.Error("a kubeconfig issued a minute ago with a one-hour lifetime was treated as expired; upgrading would rotate every existing kubeconfig")
	}
}

func TestKubeconfigExpiryOnCreate(t *testing.T) {
	if _, _, ok := kubeconfigExpiry(kubeconfigModel("", "", 3600, 0)); ok {
		t.Error("a resource with no recorded timestamps has no expiry to act on")
	}
	if diags := expiryDiagnostics(kubeconfigModel("", "", 3600, 0)); diags.HasError() {
		t.Errorf("a create must not raise an error: %v", diags)
	}
}

func TestKubeconfigExpiryRejectsUnparsableTimestamps(t *testing.T) {
	for _, tc := range []struct{ name, expiresAt, lastApplied, wantAttr string }{
		{"bad expires_at", "not-a-time", "", "expires_at"},
		{"bad last_applied", "", "not-a-time", "last_applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := kubeconfigModel(tc.expiresAt, tc.lastApplied, 3600, 0)
			if _, attr, ok := kubeconfigExpiry(m); ok || attr != tc.wantAttr {
				t.Errorf("kubeconfigExpiry() ok=%v attr=%q, want false and %q", ok, attr, tc.wantAttr)
			}
			if diags := expiryDiagnostics(m); !diags.HasError() {
				t.Error("expected an error diagnostic naming the bad timestamp")
			}
		})
	}
}

// TestKubeconfigRenewalWindow checks the comparison ModifyPlan makes.
func TestKubeconfigRenewalWindow(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name        string
		expiresAt   time.Time
		renewBefore int64
		wantRotate  bool
	}{
		{"fresh", now.Add(time.Hour), 0, false},
		{"expired", now.Add(-time.Minute), 0, true},
		{"inside the renewal window", now.Add(5 * time.Minute), 600, true},
		{"outside the renewal window", now.Add(30 * time.Minute), 600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := kubeconfigModel(tc.expiresAt.Format(time.RFC3339), "", 3600, tc.renewBefore)
			expiry, _, ok := kubeconfigExpiry(m)
			if !ok {
				t.Fatal("expected an expiry")
			}
			renewAt := expiry.Add(-time.Duration(tc.renewBefore) * time.Second)
			if got := time.Now().After(renewAt); got != tc.wantRotate {
				t.Errorf("rotate = %v, want %v (expires %s, renew at %s)", got, tc.wantRotate,
					expiry.Format(time.RFC3339), renewAt.Format(time.RFC3339))
			}
		})
	}
}

// TestOptionalTaintValue covers the other API change: a taint value may now be
// omitted, which is valid in Kubernetes (key and effect are enough).
//
// The generated schema still marks value Required, so a missing value must map
// to "" and never to null — null in a required attribute cannot be matched by
// any config a practitioner is allowed to write.
func TestOptionalTaintValue(t *testing.T) {
	got := optionalTaintValue(nil)
	if got.IsNull() {
		t.Error("a missing taint value mapped to null, but the schema marks value Required")
	}
	if got.ValueString() != "" {
		t.Errorf("a missing taint value = %q, want an empty string", got.ValueString())
	}
	v := "prod"
	if got := optionalTaintValue(&v); got.ValueString() != "prod" {
		t.Errorf("taint value = %q, want %q", got.ValueString(), "prod")
	}
	empty := ""
	if got := optionalTaintValue(&empty); got.IsNull() || got.ValueString() != "" {
		t.Errorf("an explicit empty value must stay an empty string, got %v", got)
	}
}

// TestTaintValueIsStillRequiredInTheSchema pins the constraint the mapping
// above depends on. The schema is generated from the API spec; if it is ever
// regenerated with value optional, this fails and both the "" mapping and the
// docs note that tells practitioners to write value = "" can be revisited.
func TestTaintValueIsStillRequiredInTheSchema(t *testing.T) {
	s := resource_gardener_shoot.GardenerShootResourceSchema(context.Background())

	attr, err := s.AttributeAtPath(context.Background(),
		path.Root("shoot_provider").AtName("workers").AtListIndex(0).AtName("taints").AtListIndex(0).AtName("value"))
	if err != nil {
		t.Fatalf("could not reach shoot_provider.workers[*].taints[*].value in the schema: %v", err)
	}
	if !attr.IsRequired() {
		t.Error("taint value is no longer Required: optionalTaintValue should map a missing value to null, " +
			"and the gardener_shoot docs note about writing value = \"\" is now wrong")
	}
}

// TestRotationWarningDistinguishesRenewalFromExpiry pins the summary a user
// reads first. Rotating inside renew_before_expiry_seconds happens while the
// credential is still valid, so reporting it as expired sends the user
// chasing a broken cluster credential that in fact still works.
func TestRotationWarningDistinguishesRenewalFromExpiry(t *testing.T) {
	expiry := time.Date(2026, 9, 15, 9, 31, 28, 0, time.UTC)

	for _, tc := range []struct {
		name        string
		now         time.Time
		wantSummary string
		wantIn      string
		wantNotIn   string
	}{
		{
			// The live case: renew_before_expiry_seconds = 60 fires 38s early.
			name:        "inside the renewal window the credential has not expired",
			now:         expiry.Add(-38 * time.Second),
			wantSummary: "Kubeconfig is due for renewal",
			wantIn:      "stays valid until then",
			wantNotIn:   "expired at",
		},
		{
			// The default, renew_before_expiry_seconds = 0.
			name:        "past expiry it really has expired",
			now:         expiry.Add(time.Second),
			wantSummary: "Kubeconfig expired",
			wantIn:      "expired at 2026-09-15T09:31:28Z",
			wantNotIn:   "stays valid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary, detail := rotationWarning(tc.now, expiry)
			if summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tc.wantSummary)
			}
			if !strings.Contains(detail, tc.wantIn) {
				t.Errorf("detail %q does not contain %q", detail, tc.wantIn)
			}
			if strings.Contains(detail, tc.wantNotIn) {
				t.Errorf("detail %q must not contain %q", detail, tc.wantNotIn)
			}
		})
	}
}
