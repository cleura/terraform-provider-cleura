package provider

import (
	"fmt"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// openstackIdentityConfig renders the shared test configuration: a project, a
// user, the user's role assignment on the project, and both data sources.
// Step 1 creates everything; step 2 renames the project, clears its
// description and disables it, rotates the user's password while editing its
// description, and swaps the assigned roles (one revoked, two granted).
//
// existingProjectID, when set, replaces the managed project with a data source
// lookup of that project: the live account has a hard project quota that
// disabled (never deletable) projects count against, so live runs reuse one.
func openstackIdentityConfig(providerBlock, projectName, userName string, step2, withData bool, existingProjectID string) string {
	project := fmt.Sprintf(`
resource "cleura_openstack_project" "test" {
  name        = %q
  description = "created by the provider test suite"
}`, projectName)
	projectRef := "cleura_openstack_project.test.id"
	roles := `["member"]`
	if step2 {
		roles = `["swiftoperator", "load-balancer_member"]`
	}
	user := fmt.Sprintf(`
resource "cleura_openstack_user" "test" {
  name                = %q
  password            = "Init1al-Passw0rd"
  password_wo_version = "1"
  description         = "test service account"
}`, userName)
	if step2 {
		project = fmt.Sprintf(`
resource "cleura_openstack_project" "test" {
  name    = %q
  enabled = false
}`, projectName+"-renamed")
		user = fmt.Sprintf(`
resource "cleura_openstack_user" "test" {
  name                = %q
  password            = "R0tated-Passw0rd"
  password_wo_version = "2"
  description         = "rotated"
}`, userName)
	}
	byName := `
# depends_on defers the read until the project exists (its name is known at
# plan time, so Terraform would otherwise read it before the apply).
data "cleura_openstack_project" "by_name" {
  name       = cleura_openstack_project.test.name
  depends_on = [cleura_openstack_project.test]
}`
	if existingProjectID != "" {
		project = fmt.Sprintf(`
data "cleura_openstack_project" "existing" {
  id = %q
}`, existingProjectID)
		projectRef = "data.cleura_openstack_project.existing.id"
		byName = ""
	}
	assignment := fmt.Sprintf(`
resource "cleura_openstack_role_assignment" "test" {
  user_id    = cleura_openstack_user.test.id
  project_id = %s
  roles      = %s
}`, projectRef, roles)
	if !withData {
		return providerBlock + project + user + assignment
	}
	return providerBlock + project + user + assignment + byName + `

data "cleura_openstack_user" "by_id" {
  id = cleura_openstack_user.test.id
}
`
}

// TestOpenStackIdentityLifecycle drives the resources and data sources through
// the real Terraform binary against the in-memory mock API: create, refresh
// with the project hidden from the listing, update (including the write-only
// password rotation), import, and destroy (which disables the project).
func TestOpenStackIdentityLifecycle(t *testing.T) {
	mock := newMockIdentity()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)

	const providerBlock = `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}`
	step1 := openstackIdentityConfig(providerBlock, "tftest-project", "tftest-user", false, true, "")
	// Same resources without the data sources: the by-name lookup is meant to
	// fail for a project the API user cannot see, which is what the hidden
	// refresh step simulates for the resource.
	step1NoData := openstackIdentityConfig(providerBlock, "tftest-project", "tftest-user", false, false, "")
	step2 := openstackIdentityConfig(providerBlock, "tftest-project", "tftest-user", true, true, "")

	passwordIs := func(user, want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			u := mock.userByName(user)
			if u == nil {
				return fmt.Errorf("user %q not found in the mock API", user)
			}
			if u.Password != want {
				return fmt.Errorf("user %q password = %q, want %q", user, u.Password, want)
			}
			return nil
		}
	}
	rolesAre := func(user string, want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			p := mock.projectByName("tftest-project")
			if p == nil {
				p = mock.projectByName("tftest-project-renamed")
			}
			if p == nil {
				return fmt.Errorf("project not found in the mock API")
			}
			got := mock.heldRoleNames(user, p.Id)
			sort.Strings(got)
			sort.Strings(want)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				return fmt.Errorf("user %q roles = %v, want %v", user, got, want)
			}
			return nil
		}
	}
	hideProject := func(hidden bool) func() {
		return func() {
			p := mock.projectByName("tftest-project")
			if p == nil {
				t.Fatal("project missing from the mock API")
			}
			mock.mu.Lock()
			mock.hidden[p.Id] = hidden
			mock.mu.Unlock()
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// The API cannot delete projects: destroy must leave it disabled.
			mock.mu.Lock()
			defer mock.mu.Unlock()
			if len(mock.projects) != 1 {
				return fmt.Errorf("expected the single test project to still exist (disabled) after destroy, found %d", len(mock.projects))
			}
			for _, p := range mock.projects {
				if p.Enabled {
					return fmt.Errorf("project %q should be disabled after destroy", p.Name)
				}
			}
			if len(mock.users) != 0 {
				return fmt.Errorf("user should be deleted after destroy")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: step1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("cleura_openstack_project.test", "id"),
					resource.TestCheckResourceAttr("cleura_openstack_project.test", "domain_id", mock.domainID),
					resource.TestCheckResourceAttr("cleura_openstack_project.test", "name", "tftest-project"),
					resource.TestCheckResourceAttr("cleura_openstack_project.test", "description", "created by the provider test suite"),
					resource.TestCheckResourceAttr("cleura_openstack_project.test", "enabled", "true"),

					resource.TestCheckResourceAttrSet("cleura_openstack_user.test", "id"),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "domain_id", mock.domainID),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "name", "tftest-user"),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "description", "test service account"),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "enabled", "true"),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "password_wo_version", "1"),
					// Write-only: never in state.
					resource.TestCheckNoResourceAttr("cleura_openstack_user.test", "password"),
					passwordIs("tftest-user", "Init1al-Passw0rd"),

					resource.TestCheckResourceAttrPair("cleura_openstack_role_assignment.test", "user_id", "cleura_openstack_user.test", "id"),
					resource.TestCheckResourceAttrPair("cleura_openstack_role_assignment.test", "project_id", "cleura_openstack_project.test", "id"),
					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.test", "domain_id", mock.domainID),
					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.test", "roles.#", "1"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.test", "roles.*", "member"),
					rolesAre("tftest-user", "member"),

					resource.TestCheckResourceAttrPair("data.cleura_openstack_project.by_name", "id", "cleura_openstack_project.test", "id"),
					resource.TestCheckResourceAttrPair("data.cleura_openstack_project.by_name", "domain_id", "cleura_openstack_project.test", "domain_id"),
					resource.TestCheckResourceAttr("data.cleura_openstack_project.by_name", "description", "created by the provider test suite"),
					resource.TestCheckResourceAttrPair("data.cleura_openstack_user.by_id", "name", "cleura_openstack_user.test", "name"),
					resource.TestCheckResourceAttr("data.cleura_openstack_user.by_id", "enabled", "true"),
				),
			},
			{
				// A project the API user can no longer see must not be planned
				// for recreation: Read probes it and keeps the prior state.
				PreConfig: hideProject(true),
				Config:    step1NoData,
				PlanOnly:  true,
			},
			{
				PreConfig: hideProject(false),
				Config:    step2,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cleura_openstack_project.test", "name", "tftest-project-renamed"),
					resource.TestCheckNoResourceAttr("cleura_openstack_project.test", "description"),
					resource.TestCheckResourceAttr("cleura_openstack_project.test", "enabled", "false"),

					resource.TestCheckResourceAttr("cleura_openstack_user.test", "description", "rotated"),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "password_wo_version", "2"),
					passwordIs("tftest-user", "R0tated-Passw0rd"),

					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.test", "roles.#", "2"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.test", "roles.*", "swiftoperator"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.test", "roles.*", "load-balancer_member"),
					rolesAre("tftest-user", "swiftoperator", "load-balancer_member"),
				),
			},
			{
				ResourceName:      "cleura_openstack_role_assignment.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["cleura_openstack_role_assignment.test"]
					return rs.Primary.Attributes["user_id"] + "/" + rs.Primary.Attributes["project_id"], nil
				},
			},
			{
				ResourceName:      "cleura_openstack_project.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:            "cleura_openstack_user.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password_wo_version"},
			},
		},
	})
}

// TestAccOpenStackIdentity runs the same lifecycle against a live Cleura
// account (TF_ACC=1 plus the CLEURA_* credential, cloud/url, and region
// variables). Note that destroy can only disable the project, so each run
// leaves one disabled, randomly named project behind (unless
// CLEURA_TEST_OPENSTACK_PROJECT_ID points at an existing one).
//
// Known to be flaky until the API reads its own writes: the identity listings
// lag writes by minutes (.agent/cleura-api-wishlist-openstack-identity.md item
// 21), so the post-apply refresh may see a revoked role as still held.
func TestAccOpenStackIdentity(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests require TF_ACC=1")
	}
	for _, v := range []string{"CLEURA_API_USERNAME", "CLEURA_API_TOKEN", "CLEURA_REGION"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set for acceptance tests", v)
		}
	}
	if os.Getenv("CLEURA_API_URL") == "" && os.Getenv("CLEURA_CLOUD") == "" {
		t.Fatal("Set CLEURA_CLOUD or CLEURA_API_URL for acceptance tests")
	}

	suffix := acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum)
	projectName := "tfacc-" + suffix
	userName := "tfacc-" + suffix
	// Disabled projects count against the account's project quota and cannot be
	// deleted, so by default the live run assigns roles on an existing project
	// instead of creating one. Unset this to exercise project creation.
	existingProject := os.Getenv("CLEURA_TEST_OPENSTACK_PROJECT_ID")
	const providerBlock = `
provider "cleura" {
  use_cli = false
}`
	projectChecks := func(step2 bool) []resource.TestCheckFunc {
		if existingProject != "" {
			return []resource.TestCheckFunc{
				resource.TestCheckResourceAttr("data.cleura_openstack_project.existing", "id", existingProject),
				resource.TestCheckResourceAttr("cleura_openstack_role_assignment.test", "project_id", existingProject),
			}
		}
		if step2 {
			return []resource.TestCheckFunc{
				resource.TestCheckResourceAttr("cleura_openstack_project.test", "name", projectName+"-renamed"),
				resource.TestCheckNoResourceAttr("cleura_openstack_project.test", "description"),
				resource.TestCheckResourceAttr("cleura_openstack_project.test", "enabled", "false"),
			}
		}
		return []resource.TestCheckFunc{
			resource.TestCheckResourceAttrSet("cleura_openstack_project.test", "id"),
			resource.TestCheckResourceAttrSet("cleura_openstack_project.test", "domain_id"),
			resource.TestCheckResourceAttr("cleura_openstack_project.test", "name", projectName),
			resource.TestCheckResourceAttr("cleura_openstack_project.test", "enabled", "true"),
			resource.TestCheckResourceAttrPair("data.cleura_openstack_project.by_name", "id", "cleura_openstack_project.test", "id"),
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: openstackIdentityConfig(providerBlock, projectName, userName, false, true, existingProject),
				// KNOWN FLAKE, not a provider bug: the identity listings lag
				// writes by minutes and alternate between replicas, so the
				// refresh plan the framework runs after an apply occasionally
				// still shows the change that was just applied and this step
				// fails with a non-empty plan. Re-run it a few minutes later.
				// The assertion is deliberately left strict so a real
				// non-converging resource is still caught.
				Check: resource.ComposeAggregateTestCheckFunc(append(projectChecks(false),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "name", userName),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "description", "test service account"),
					resource.TestCheckNoResourceAttr("cleura_openstack_user.test", "password"),
					resource.TestCheckResourceAttrPair("data.cleura_openstack_user.by_id", "name", "cleura_openstack_user.test", "name"),
					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.test", "roles.#", "1"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.test", "roles.*", "member"),
				)...),
			},
			{
				Config: openstackIdentityConfig(providerBlock, projectName, userName, true, true, existingProject),
				Check: resource.ComposeAggregateTestCheckFunc(append(projectChecks(true),
					resource.TestCheckResourceAttr("cleura_openstack_user.test", "description", "rotated"),
					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.test", "roles.#", "2"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.test", "roles.*", "swiftoperator"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.test", "roles.*", "load-balancer_member"),
				)...),
			},
			{
				ResourceName:      "cleura_openstack_role_assignment.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["cleura_openstack_role_assignment.test"]
					return rs.Primary.Attributes["user_id"] + "/" + rs.Primary.Attributes["project_id"], nil
				},
			},
			{
				ResourceName:            "cleura_openstack_user.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password_wo_version"},
			},
		},
	})
}
