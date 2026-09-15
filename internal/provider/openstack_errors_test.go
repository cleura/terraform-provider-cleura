package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// mockEnv points the provider at a fresh mock API and returns it.
func mockEnv(t *testing.T) *mockIdentity {
	t.Helper()
	mock := newMockIdentity()
	t.Setenv("CLEURA_API_URL", newTestServer(t, mock.handler()))
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)
	return mock
}

const errProvider = `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}
`

// errorCase drives one config to failure and asserts the message a customer
// sees. These are the diagnostics that decide whether the provider feels
// well-built, so they are asserted like any other behaviour.
func errorCase(t *testing.T, name, config string, want *regexp.Regexp) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		mockEnv(t)
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps:                    []resource.TestStep{{Config: errProvider + config, ExpectError: want}},
		})
	})
}

func TestOpenStackDataSourceSelectorErrors(t *testing.T) {
	// Neither id nor name: the validator must name both attributes.
	errorCase(t, "project_neither", `
data "cleura_openstack_project" "x" {}
`, regexp.MustCompile(`(?s)Missing Attribute Configuration.*Exactly one of these attributes must be configured.*id.*name`))

	// Both id and name: same validator, other direction.
	errorCase(t, "project_both", `
data "cleura_openstack_project" "x" {
  id   = "0123456789abcdef0123456789abcdef"
  name = "whatever"
}
`, regexp.MustCompile(`(?s)Invalid Attribute Combination`))

	errorCase(t, "user_neither", `
data "cleura_openstack_user" "x" {}
`, regexp.MustCompile(`(?s)Missing Attribute Configuration.*Exactly one of these attributes must be configured.*id.*name`))
}

func TestOpenStackDataSourceNotFoundErrors(t *testing.T) {
	// A name that does not exist must say so, and say where it looked.
	errorCase(t, "project_by_name", `
data "cleura_openstack_project" "x" {
  name = "no-such-project"
}
`, regexp.MustCompile(`(?s)OpenStack project not found.*no-such-project`))

	errorCase(t, "project_by_id", `
data "cleura_openstack_project" "x" {
  id = "ffffffffffffffffffffffffffffffff"
}
`, regexp.MustCompile(`(?s)OpenStack project not found.*ffffffff`))

	errorCase(t, "user_by_name", `
data "cleura_openstack_user" "x" {
  name = "no-such-user"
}
`, regexp.MustCompile(`(?s)OpenStack user not found.*no-such-user.*domain`))

	errorCase(t, "user_by_id", `
data "cleura_openstack_user" "x" {
  id = "ffffffffffffffffffffffffffffffff"
}
`, regexp.MustCompile(`(?s)OpenStack user not found`))
}

func TestOpenStackRoleAssignmentErrors(t *testing.T) {
	// An unknown role name must be caught with the valid names listed, not
	// forwarded to the API as an opaque 404.
	errorCase(t, "unknown_role", `
resource "cleura_openstack_user" "u" {
  name                = "err-user"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
}

resource "cleura_openstack_role_assignment" "ra" {
  user_id    = cleura_openstack_user.u.id
  project_id = "0123456789abcdef0123456789abcdef"
  roles      = ["administrator"]
}
`, regexp.MustCompile(`(?s)Unknown OpenStack role.*administrator.*available roles.*member`))

	// An empty roles set is a configuration error, not an apply-time surprise.
	errorCase(t, "empty_roles", `
resource "cleura_openstack_role_assignment" "ra" {
  user_id    = "0123456789abcdef0123456789abcdef"
  project_id = "0123456789abcdef0123456789abcdef"
  roles      = []
}
`, regexp.MustCompile(`(?s)(at least 1|Invalid Attribute Value)`))

	// A project that does not exist: the API's 404 must reach the user with the
	// project id in it.
	errorCase(t, "unknown_project", `
resource "cleura_openstack_user" "u" {
  name                = "err-user2"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
}

resource "cleura_openstack_role_assignment" "ra" {
  user_id    = cleura_openstack_user.u.id
  project_id = "ffffffffffffffffffffffffffffffff"
  roles      = ["member"]
}
`, regexp.MustCompile(`(?s)Failed to grant OpenStack project access.*ffffffff`))
}

func TestOpenStackValidatorErrors(t *testing.T) {
	// The user name pattern is enforced at plan time with a readable message.
	errorCase(t, "user_name_uppercase", `
resource "cleura_openstack_user" "u" {
  name                = "Not-Lowercase"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
}
`, regexp.MustCompile(`(?s)lowercase letters, digits`))

	errorCase(t, "user_name_too_short", `
resource "cleura_openstack_user" "u" {
  name                = "ab"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
}
`, regexp.MustCompile(`(?s)3-40 characters`))

	errorCase(t, "password_too_short", `
resource "cleura_openstack_user" "u" {
  name                = "shortpw"
  password            = "short"
  password_wo_version = "1"
}
`, regexp.MustCompile(`(?s)between 8 and 1024`))

	// Project names allow spaces and Swedish letters, but not slashes.
	errorCase(t, "project_name_invalid", `
resource "cleura_openstack_project" "p" {
  name = "bad/name"
}
`, regexp.MustCompile(`(?s)letters, digits, spaces`))

	// An empty description is a mistake worth catching: the API stores "" as
	// "no description", so the attribute must be omitted instead.
	errorCase(t, "empty_description", `
resource "cleura_openstack_project" "p" {
  name        = "desc-test"
  description = ""
}
`, regexp.MustCompile(`(?s)(at least 1|Invalid Attribute Value)`))
}

func TestOpenStackProjectDuplicateNameError(t *testing.T) {
	// The mock rejects a duplicate project name the way the API does; the
	// message must reach the user rather than being swallowed.
	t.Run("duplicate_name", func(t *testing.T) {
		mock := mockEnv(t)
		cfg := fmt.Sprintf(errProvider+`
resource "cleura_openstack_project" "a" {
  name = %q
}

resource "cleura_openstack_project" "b" {
  name       = %q
  depends_on = [cleura_openstack_project.a]
}
`, "dup-name", "dup-name")
		_ = mock
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`(?s)Failed to create OpenStack project.*already in use`),
			}},
		})
	})
}
