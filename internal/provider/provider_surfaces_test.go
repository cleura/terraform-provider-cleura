package provider

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testSurfaceProjectID is the project_id the shared-provider tests configure.
const testSurfaceProjectID = "5bc1a0e0a2fa4a6b9f4b8f0f2ba7b1cd"

// TestOpenStackResourcesIgnoreProjectID covers the surfaces sharing one
// ProviderConfig. Gardener resources require project_id and OpenStack identity
// resources must not: a customer who configures the provider for Gardener has
// project_id set, and that must neither be demanded of nor applied to the
// identity resources, which are domain-scoped rather than project-scoped.
func TestOpenStackResourcesIgnoreProjectID(t *testing.T) {
	mock := newMockIdentity()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)

	config := `
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "5bc1a0e0a2fa4a6b9f4b8f0f2ba7b1cd"
  use_cli    = false
}

resource "cleura_openstack_user" "shared" {
  name                = "tfsurface-user"
  password            = "Init1al-Passw0rd"
  password_wo_version = "1"
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("cleura_openstack_user.shared", "name", "tfsurface-user"),
				resource.TestCheckResourceAttr("cleura_openstack_user.shared", "domain_id", mock.domainID),
				// default_project_id tracks the API's own value for the user,
				// and must not be back-filled from the provider's project_id.
				resource.TestCheckNoResourceAttr("cleura_openstack_user.shared", "default_project_id"),
			),
		}},
	})
}

// TestGardenerStillRequiresProjectID is the other half: the shared config
// object must keep failing Gardener resources cleanly when project_id is
// absent, with a message naming the attribute and its environment variable.
func TestGardenerStillRequiresProjectID(t *testing.T) {
	mock := newMockIdentity()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)
	t.Setenv("CLEURA_PROJECT_ID", "")

	config := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

resource "cleura_gardener_shoot_kubeconfig" "no_project" {
  shoot_name         = "tfsurface-shoot"
  expiration_seconds = 3600
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)Missing Cleura project_id.*CLEURA_PROJECT_ID`),
		}},
	})
}

// TestBothSurfacesConfigureFromOneProvider asserts the two surfaces coexist in
// one configuration behind one provider instance. The Gardener resource must
// get past provider configuration and fail at its own API call instead — the
// mock serves no Gardener routes, so a 404 from there is the proof that the
// shared ProviderConfig served it correctly.
func TestBothSurfacesConfigureFromOneProvider(t *testing.T) {
	mock := newMockIdentity()

	// The framework destroys everything after the step, so record the identity
	// call as it happens rather than looking for the user afterwards.
	var mu sync.Mutex
	identityCalled := false
	gardenerPath := ""
	inner := mock.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/users"):
			identityCalled = true
		case strings.Contains(r.URL.Path, "/gardener/"):
			gardenerPath = r.URL.Path
		}
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)

	config := `
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "5bc1a0e0a2fa4a6b9f4b8f0f2ba7b1cd"
  use_cli    = false
}

resource "cleura_openstack_user" "with_gardener" {
  name                = "tfsurface-both"
  password            = "Init1al-Passw0rd"
  password_wo_version = "1"
}

resource "cleura_gardener_shoot_kubeconfig" "with_identity" {
  shoot_name         = "tfsurface-shoot"
  expiration_seconds = 3600
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			// The kubeconfig resource only reports "API error <status>" after a
			// completed HTTP round trip; a transport failure and the two
			// provider-level refusals all carry different titles. Matching it
			// therefore proves the request was built and sent.
			ExpectError: regexp.MustCompile(`API error 404`),
		}},
	})

	mu.Lock()
	defer mu.Unlock()
	if !identityCalled {
		t.Error("no OpenStack user create reached the API: the identity surface did not run alongside Gardener")
	}
	if gardenerPath == "" {
		t.Fatal("no Gardener request reached the API: the Gardener surface never dispatched")
	}
	// The project id in the request path can only have come from the shared
	// ProviderConfig, which the identity resources were using at the same time.
	if !strings.Contains(gardenerPath, testSurfaceProjectID) {
		t.Errorf("Gardener request path %q does not carry the provider's project_id", gardenerPath)
	}
}
