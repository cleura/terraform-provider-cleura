package provider

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Both resources are created with one call and then completed with a second
// (the API cannot set enabled on create, and ignores a user's description).
// If that second call fails, the object already exists remotely, so it MUST be
// in Terraform state: otherwise the next apply creates a duplicate — which for
// a project can never be undone.
func TestOpenStackCreateIsRecoverableWhenTheFollowUpFails(t *testing.T) {
	t.Run("project_disable_fails", func(t *testing.T) {
		mock := mockEnv(t)
		config := errProvider + `
resource "cleura_openstack_project" "p" {
  name    = "fault-project"
  enabled = false
}
`
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				// The create succeeds, the follow-up disable does not. Terraform
				// taints the resource and its automatic retry is a REPLACE, which
				// cannot work for an undeletable project whose name is taken — so
				// the diagnostic has to hand the user the manual recovery.
				PreConfig:   func() { mock.injectFaultOnce("PATCH /openstack/identity/v2/domains", 500) },
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)Failed to disable the OpenStack project after creating it.*was created.*tainted.*terraform untaint`),
			}},
		})
		// The failed apply must not have leaked a second project: a duplicate
		// could never be cleaned up.
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if len(mock.projects) != 1 {
			t.Errorf("%d projects exist after the failed apply, want exactly 1", len(mock.projects))
		}
	})

	t.Run("user_description_fails", func(t *testing.T) {
		mock := mockEnv(t)
		config := errProvider + `
resource "cleura_openstack_user" "u" {
  name                = "fault-user"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
  description         = "needs a follow-up patch"
}
`
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					PreConfig:   func() { mock.injectFaultOnce("PATCH /openstack/identity/v2/domains", 500) },
					Config:      config,
					ExpectError: regexp.MustCompile(`(?s)Failed to apply the OpenStack user's description or enabled state after creating it`),
				},
				{
					// A user CAN be deleted, so Terraform's automatic replace of
					// the tainted resource recovers on its own.
					Config: config,
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr("cleura_openstack_user.u", "description", "needs a follow-up patch"),
						func(*terraform.State) error {
							mock.mu.Lock()
							defer mock.mu.Unlock()
							if len(mock.users) != 1 {
								return fmt.Errorf("%d users exist after the failed apply and recovery, want 1", len(mock.users))
							}
							return nil
						},
					),
				},
			},
		})
	})
}

// Drift: an object deleted outside Terraform must be planned for recreation,
// not reported as an error on every refresh.
func TestOpenStackDriftHandling(t *testing.T) {
	t.Run("user_deleted_out_of_band", func(t *testing.T) {
		mock := mockEnv(t)
		config := errProvider + `
resource "cleura_openstack_user" "u" {
  name                = "drift-user"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
}
`
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config},
				{
					// Delete the user behind Terraform's back; the next plan
					// must want to create it again.
					PreConfig: func() {
						mock.mu.Lock()
						defer mock.mu.Unlock()
						for id := range mock.users {
							delete(mock.users, id)
						}
					},
					Config:             config,
					PlanOnly:           true,
					ExpectNonEmptyPlan: true,
				},
			},
		})
	})

	t.Run("role_revoked_out_of_band", func(t *testing.T) {
		mock := mockEnv(t)
		config := errProvider + `
resource "cleura_openstack_project" "p" {
  name = "drift-project"
}

resource "cleura_openstack_user" "u" {
  name                = "drift-user2"
  password            = "Some-Passw0rd"
  password_wo_version = "1"
}

resource "cleura_openstack_role_assignment" "ra" {
  user_id    = cleura_openstack_user.u.id
  project_id = cleura_openstack_project.p.id
  roles      = ["member"]
}
`
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: config},
				{
					// Someone revokes the grant in the console: Terraform must
					// notice and plan to restore it.
					PreConfig: func() {
						mock.mu.Lock()
						defer mock.mu.Unlock()
						for _, u := range mock.users {
							u.grants = map[string]map[string]time.Time{}
						}
					},
					Config:             config,
					PlanOnly:           true,
					ExpectNonEmptyPlan: true,
				},
			},
		})
	})
}
