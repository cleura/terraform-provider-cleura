package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestGardenerBootstrapSchemaIsValid(t *testing.T) {
	ctx := context.Background()
	var resp fwresource.SchemaResponse
	NewGardenerBootstrapResource().Schema(ctx, fwresource.SchemaRequest{}, &resp)
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Errorf("schema is invalid: %v", diags)
	}
}

func TestBootstrapID(t *testing.T) {
	if got, want := bootstrapID("public", "Sto2", "abc"), "public/Sto2/abc"; got != want {
		t.Errorf("bootstrapID() = %q, want %q", got, want)
	}
}

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

func bootstrapTestServer(t *testing.T, mock *mockIdentity) {
	t.Helper()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)
	t.Setenv("CLEURA_PROJECT_ID", "")
}

// TestGardenerBootstrapLifecycle covers the shape the API forces on this
// resource: one POST at create, nothing on refresh, nothing on destroy. It
// also covers the flow the resource exists for — bootstrapping a project
// created in the same configuration, which the provider's own project_id
// cannot express without a dependency cycle.
func TestGardenerBootstrapLifecycle(t *testing.T) {
	mock := newMockIdentity()
	bootstrapTestServer(t, mock)

	config := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

resource "cleura_openstack_project" "new" {
  name = "tfboot-project"
}

resource "cleura_gardener_bootstrap" "new" {
  project_id = cleura_openstack_project.new.id
}`

	projectWasBootstrapped := func(*terraform.State) error {
		p := mock.projectByName("tfboot-project")
		if p == nil {
			return fmt.Errorf("project missing from the mock API")
		}
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if !mock.bootstrapped[p.Id] {
			return fmt.Errorf("project %s was never bootstrapped", p.Id)
		}
		if mock.bootstrapCalls != 1 {
			return fmt.Errorf("bootstrap was called %d times, want exactly 1", mock.bootstrapCalls)
		}
		return nil
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// Destroy must not call the API: there is no teardown endpoint,
			// and a stray call would be a request against a real project.
			mock.mu.Lock()
			defer mock.mu.Unlock()
			if mock.bootstrapCalls != 1 {
				return fmt.Errorf("bootstrap was called %d times across the run, want exactly 1 (destroy must not call the API)", mock.bootstrapCalls)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("cleura_gardener_bootstrap.new", "project_id", "cleura_openstack_project.new", "id"),
					resource.TestCheckResourceAttr("cleura_gardener_bootstrap.new", "cloud", "public"),
					resource.TestCheckResourceAttr("cleura_gardener_bootstrap.new", "region", "Sto2"),
					resource.TestCheckResourceAttrSet("cleura_gardener_bootstrap.new", "id"),
					projectWasBootstrapped,
				),
			},
			{
				// Importing records an existing bootstrap; nothing is verified
				// because the API has no endpoint to verify against.
				ResourceName: "cleura_gardener_bootstrap.new",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["cleura_gardener_bootstrap.new"]
					if !ok {
						return "", fmt.Errorf("resource not found in state")
					}
					return rs.Primary.Attributes["project_id"], nil
				},
				ImportStateVerify: true,
			},
		},
	})
}

// TestGardenerBootstrapUsesProviderProjectID covers the common case: no
// project_id on the resource, so it falls back to the provider's.
func TestGardenerBootstrapUsesProviderProjectID(t *testing.T) {
	mock := newMockIdentity()
	bootstrapTestServer(t, mock)

	// A project the provider can point at without creating one in the run.
	existing := mock.addProject("tfboot-existing")

	config := fmt.Sprintf(`
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = %q
  use_cli    = false
}

resource "cleura_gardener_bootstrap" "default" {}`, existing)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("cleura_gardener_bootstrap.default", "project_id", existing),
				resource.TestCheckResourceAttr("cleura_gardener_bootstrap.default", "id", "public/Sto2/"+existing),
				func(*terraform.State) error {
					mock.mu.Lock()
					defer mock.mu.Unlock()
					if !mock.bootstrapped[existing] {
						return fmt.Errorf("project %s was never bootstrapped", existing)
					}
					return nil
				},
			),
		}},
	})
}

// TestGardenerBootstrapWithoutAnyProjectID checks the error a user gets when
// neither the resource nor the provider names a project.
func TestGardenerBootstrapWithoutAnyProjectID(t *testing.T) {
	mock := newMockIdentity()
	bootstrapTestServer(t, mock)

	config := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

resource "cleura_gardener_bootstrap" "nowhere" {}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)Missing project_id.*CLEURA_PROJECT_ID`),
		}},
	})
}

// TestGardenerBootstrapUnknownProjectIsExplained is the DX case behind the
// workaround: live, an unknown project id comes back as an opaque 500.
func TestGardenerBootstrapUnknownProjectIsExplained(t *testing.T) {
	mock := newMockIdentity()
	bootstrapTestServer(t, mock)

	config := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

resource "cleura_gardener_bootstrap" "typo" {
  project_id = "00000000000000000000000000000000"
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`(?s)HTTP 500.*unknown project ID`),
		}},
	})
}

// TestGardenerBootstrapRejectsEmptyProjectID covers the config validator.
func TestGardenerBootstrapRejectsEmptyProjectID(t *testing.T) {
	mock := newMockIdentity()
	bootstrapTestServer(t, mock)

	config := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

resource "cleura_gardener_bootstrap" "empty" {
  project_id = ""
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			ExpectError: regexp.MustCompile(`Empty project_id`),
		}},
	})
}
