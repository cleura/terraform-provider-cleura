package provider

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// TestBootstrapErrorDetail pins the guidance added to the API's opaque 500.
// An unknown project id is answered with 500 rather than 404 live, so the
// status alone would send a user looking for an outage.
func TestBootstrapErrorDetail(t *testing.T) {
	body := []byte(`{"error":{"code":500,"message":"Internal Server Error"}}`)
	detail := bootstrapErrorDetail(500, "deadbeef", body)
	for _, want := range []string{"Internal Server Error", "unknown project ID", `"deadbeef"`} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(detail) {
			t.Errorf("500 detail %q does not mention %q", detail, want)
		}
	}

	notFound := bootstrapErrorDetail(404, "deadbeef", []byte(`{"error":{"code":404,"message":"OpenStack region X was not found"}}`))
	if regexp.MustCompile("unknown project ID").MatchString(notFound) {
		t.Errorf("the unknown-project hint must not be attached to a 404: %q", notFound)
	}
	if !regexp.MustCompile("region X was not found").MatchString(notFound) {
		t.Errorf("404 detail lost the API's message: %q", notFound)
	}
}

// TestEnsureBootstrapped covers the call every shoot create now makes. The API
// has no way to report whether a project is already prepared, so the provider
// repeats the call and relies on it being a no-op the second time.
func TestEnsureBootstrapped(t *testing.T) {
	mock := newMockIdentity()
	cfg := newMockConfig(t, mock)
	cfg.ProjectID = mock.addProject("tfboot-target")

	for i := 1; i <= 3; i++ {
		if diags := ensureBootstrapped(context.Background(), cfg); diags.HasError() {
			t.Fatalf("call %d failed: %v", i, diags.Errors())
		}
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.bootstrapped[cfg.ProjectID] {
		t.Errorf("project %s was never bootstrapped", cfg.ProjectID)
	}
	// Repeating is the whole point: the provider cannot ask whether the
	// project is ready, so it must be willing to call every time.
	if mock.bootstrapCalls != 3 {
		t.Errorf("bootstrap was called %d times, want 3", mock.bootstrapCalls)
	}
}

// TestEnsureBootstrappedExplainsAnUnknownProject keeps the workaround wired
// up: live, a project id the API does not know answers an opaque 500.
func TestEnsureBootstrappedExplainsAnUnknownProject(t *testing.T) {
	mock := newMockIdentity()
	cfg := newMockConfig(t, mock)
	cfg.ProjectID = "00000000000000000000000000000000"

	diags := ensureBootstrapped(context.Background(), cfg)
	if !diags.HasError() {
		t.Fatal("expected an error for an unknown project")
	}
	detail := diags.Errors()[0].Detail()
	if !strings.Contains(detail, "unknown project ID") {
		t.Errorf("detail does not explain the 500: %q", detail)
	}
}
