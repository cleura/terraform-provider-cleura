package provider

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestCleuraProjectIsDeprecated pins the consolidation: cleura_project is
// superseded by cleura_openstack_project, and says so in a message Terraform
// shows on every plan that uses it.
func TestCleuraProjectIsDeprecated(t *testing.T) {
	ctx := context.Background()
	var resp datasource.SchemaResponse
	NewProjectDataSource().Schema(ctx, datasource.SchemaRequest{}, &resp)

	if resp.Schema.DeprecationMessage == "" {
		t.Fatal("cleura_project carries no DeprecationMessage")
	}
	if !strings.Contains(resp.Schema.DeprecationMessage, "cleura_openstack_project") {
		t.Errorf("the deprecation message does not name the replacement: %q", resp.Schema.DeprecationMessage)
	}
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Errorf("schema is invalid: %v", diags)
	}
}

// TestCleuraProjectStillWorks is the other half of deprecating rather than
// removing: v0.1.0 shipped this data source, so existing configurations must
// keep planning and applying.
func TestCleuraProjectStillWorks(t *testing.T) {
	mock := newMockIdentity()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)

	id := mock.addProject("tfdeprecated-project")

	config := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

data "cleura_project" "old" {
  name = "tfdeprecated-project"
}

data "cleura_openstack_project" "new" {
  name = "tfdeprecated-project"
}`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.cleura_project.old", "id", id),
				// The replacement resolves the same project, so a migration is
				// a rename and nothing else.
				resource.TestCheckResourceAttrPair("data.cleura_openstack_project.new", "id", "data.cleura_project.old", "id"),
			),
		}},
	})
}
