package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// twoDomainMux serves an account with two domains (Stockholm and Frankfurt)
// and a region-scoped project quota endpoint that, like the live API, answers
// 404 when the region does not belong to the domain. probedRegions records
// every region tag the quota endpoint was asked for.
func twoDomainMux(probedRegions *[]string) http.Handler {
	domainRegions := map[string]string{"dom-sto": "Sto2", "dom-fra": "Fra1"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openstack/identity/v2/domains", func(w http.ResponseWriter, r *http.Request) {
		sto, fra := "CCP_Domain_1", "CCP_Domain_2"
		writeJSON(w, 200, []api.CommonOpenStackDomain{
			{Id: "dom-sto", Name: &sto, Status: "provisioned", Area: api.CommonOpenStackDomainArea{
				Id: 1, Name: "Sweden / Stockholm", Tag: "SE_STO2",
				Regions: []api.CommonOpenStackRegion{{Id: 2, Name: "Stockholm", Tag: "Sto2"}}}},
			{Id: "dom-fra", Name: &fra, Status: "provisioned", Area: api.CommonOpenStackDomainArea{
				Id: 7, Name: "Germany / Frankfurt", Tag: "DE",
				Regions: []api.CommonOpenStackRegion{{Id: 6, Name: "Frankfurt", Tag: "Fra1"}}}},
		})
	})
	mux.HandleFunc("GET /openstack/identity/v2/domains/{domain}/projects/{project}/quotas/{region}", func(w http.ResponseWriter, r *http.Request) {
		region := r.PathValue("region")
		*probedRegions = append(*probedRegions, region)
		if domainRegions[r.PathValue("domain")] != region {
			apiError(w, 404, "Not Found: Project not found")
			return
		}
		writeJSON(w, 200, api.OpenStackIdentityProjectQuota{})
	})
	return mux
}

// A project in a domain that does not serve the provider's region must not be
// reported as gone: probing the quota endpoint with a foreign region tag answers
// 404, and the caller would then drop a live project from state and create a
// second one — which cannot be undone.
func TestProjectExistsProbesTheDomainsOwnRegion(t *testing.T) {
	var probed []string
	cfg := newTestConfig(t, twoDomainMux(&probed))
	cfg.Region = "Sto2"

	exists, err := projectExists(context.Background(), cfg, "dom-fra", "p-in-frankfurt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Errorf("project in dom-fra reported as gone; quota was probed for regions %v, want Fra1", probed)
	}
	if len(probed) != 1 || probed[0] != "Fra1" {
		t.Errorf("probed regions = %v, want [Fra1]", probed)
	}

	// The provider's own region is still preferred when the domain serves it.
	probed = nil
	if _, err := projectExists(context.Background(), cfg, "dom-sto", "p-in-stockholm"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(probed) != 1 || probed[0] != "Sto2" {
		t.Errorf("probed regions = %v, want [Sto2]", probed)
	}
}

// TestDomainRegionTagFailsRatherThanGuessing pins that an unresolvable domain
// is an error, not the provider's region. Guessing looks harmless but feeds
// projectExists a region the domain may not serve; the resulting 404 reads as
// "the project is gone", the resource is dropped from state, and the next
// apply creates a second project that can never be deleted.
func TestDomainRegionTagFailsRatherThanGuessing(t *testing.T) {
	var probed []string
	cfg := newTestConfig(t, twoDomainMux(&probed))
	cfg.Region = "Sto2"

	if got, err := domainRegionTag(context.Background(), cfg, "dom-nope"); err == nil {
		t.Errorf("an unknown domain returned %q, want an error", got)
	}

	broken := newTestConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 500, "Internal Server Error")
	}))
	broken.Region = "Kna1"
	if got, err := domainRegionTag(context.Background(), broken, "dom-sto"); err == nil {
		t.Errorf("an unavailable domain listing returned %q, want an error", got)
	}

	// The resolvable cases still answer, and answer with the domain's region.
	if got, err := domainRegionTag(context.Background(), cfg, "dom-fra"); err != nil || got != "Fra1" {
		t.Errorf("dom-fra = %q, %v; want Fra1 and no error", got, err)
	}
}

// TestProjectExistsRefusesToGuessARegion covers the conjunction that makes the
// guess dangerous: a project outside the provider's region, invisible in the
// caller's listing, while the domain listing is down. projectExists must
// report an error so Read keeps the project in state, rather than answering
// "gone" and letting Terraform create a duplicate.
func TestProjectExistsRefusesToGuessARegion(t *testing.T) {
	var probed []string
	quotaOnly := http.NewServeMux()
	quotaOnly.HandleFunc("GET /openstack/identity/v2/domains", func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 500, "Internal Server Error")
	})
	quotaOnly.HandleFunc("GET /openstack/identity/v2/domains/{domain}/projects/{project}/quotas/{region}", func(w http.ResponseWriter, r *http.Request) {
		probed = append(probed, r.PathValue("region"))
		// dom-fra serves Fra1 only, so a probe on the provider's Sto2 404s.
		apiError(w, 404, "Not Found: Project not found")
	})
	cfg := newTestConfig(t, quotaOnly)
	cfg.Region = "Sto2"

	exists, err := projectExists(context.Background(), cfg, "dom-fra", "live-project")
	if err == nil {
		t.Fatalf("projectExists returned (%v, nil); a live project would have been dropped from state", exists)
	}
	if exists {
		t.Error("projectExists should not claim existence when it could not probe")
	}
	if len(probed) != 0 {
		t.Errorf("the quota endpoint was probed with a guessed region %v; it should not have been called at all", probed)
	}
}

// revokeMux answers every revoke with the API's opaque 500 and reports the
// user's roles as heldRoles, so the caller's "was it actually revoked?" check
// can be exercised in both directions.
func revokeMux(heldRoles []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /openstack/identity/v2/domains/{domain}/users/{user}/projects/{project}/roles/{role}", func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 500, "OpenStack error occurred.")
	})
	mux.HandleFunc("GET /openstack/identity/v2/domains/{domain}/users/{user}/projects", func(w http.ResponseWriter, r *http.Request) {
		roles := make([]api.OpenStackIdentityProjectRole, 0, len(heldRoles))
		for _, n := range heldRoles {
			roles = append(roles, api.OpenStackIdentityProjectRole{Id: "id-" + n, Name: n})
		}
		writeJSON(w, 200, []api.OpenStackIdentityProjectMembership{
			{Id: "proj", Name: "proj", DomainId: "dom", Roles: &roles},
		})
	})
	return mux
}

// Revoking a role the API no longer holds answers 500; that must not fail an
// apply, because the desired state (role gone) is already true.
func TestRevokeProjectRoleTreatsAlreadyGoneAsSuccess(t *testing.T) {
	cfg := newTestConfig(t, revokeMux([]string{"swiftoperator"}))
	if err := revokeProjectRole(context.Background(), cfg, "dom", "user", "proj", "member", "id-member"); err != nil {
		t.Errorf("role already gone: want success, got %v", err)
	}
}

// A 500 while the role really is still assigned must surface, and the message
// must tell the operator what else could cause it.
func TestRevokeProjectRoleReportsAGenuineFailure(t *testing.T) {
	cfg := newTestConfig(t, revokeMux([]string{"member"}))
	err := revokeProjectRole(context.Background(), cfg, "dom", "user", "proj", "member", "id-member")
	if err == nil {
		t.Fatal("role still held: want an error, got success")
	}
	for _, want := range []string{`"member"`, "stale"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

// A cancelled context must abort the confirmation loop instead of sleeping
// through it, so Ctrl+C during an apply is responsive.
func TestRevokeProjectRoleHonoursCancellation(t *testing.T) {
	cfg := newTestConfig(t, revokeMux([]string{"member"}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := revokeProjectRole(ctx, cfg, "dom", "user", "proj", "member", "id-member"); err == nil {
		t.Error("cancelled context: want an error")
	}
}

// An explicit domain_id must be used as given, without the provider listing
// the account's domains at all.
func TestExplicitDomainIDSkipsDomainLookup(t *testing.T) {
	mock := newMockIdentity()
	cfg := newMockConfig(t, mock)

	got, err := resolveDomainID(context.Background(), cfg, mock.domainID)
	if err != nil || got != mock.domainID {
		t.Fatalf("got (%q, %v), want (%q, nil)", got, err, mock.domainID)
	}
	mock.mu.Lock()
	calls := mock.domainListCalls
	mock.mu.Unlock()
	if calls != 0 {
		t.Errorf("domain listing called %d times for an explicit domain_id, want 0", calls)
	}

	// Without an explicit domain the lookup happens once and is then cached.
	for i := 0; i < 3; i++ {
		if _, err := resolveDomainID(context.Background(), cfg, ""); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	mock.mu.Lock()
	calls = mock.domainListCalls
	mock.mu.Unlock()
	if calls != 1 {
		t.Errorf("domain listing called %d times across 3 resolves, want 1 (cached)", calls)
	}
}

// Import identifiers: the accepted forms work and every malformed form fails
// with a message that shows the expected shape.
func TestRoleAssignmentImportIDParsing(t *testing.T) {
	for _, tc := range []struct {
		id                    string
		wantErr               bool
		user, project, domain string
	}{
		{id: "u1/p1", user: "u1", project: "p1"},
		{id: " u1/p1 ", user: "u1", project: "p1"},
		{id: "d1/u1/p1", user: "u1", project: "p1", domain: "d1"},
		{id: "u1", wantErr: true},
		{id: "a/b/c/d", wantErr: true},
		{id: "/p1", wantErr: true},
		{id: "u1/", wantErr: true},
		{id: "", wantErr: true},
	} {
		t.Run(fmt.Sprintf("%q", tc.id), func(t *testing.T) {
			domain, user, project, err := parseRoleAssignmentImportID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got (%q,%q,%q)", domain, user, project)
				}
				if !strings.Contains(err.Error(), "<user_id>/<project_id>") {
					t.Errorf("error should show the expected format, got %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if user != tc.user || project != tc.project || domain != tc.domain {
				t.Errorf("got (%q,%q,%q), want (%q,%q,%q)", domain, user, project, tc.domain, tc.user, tc.project)
			}
		})
	}
}

// A second mock-backed lifecycle covering what the first one does not: an
// explicit domain_id, resources created disabled, a description added and then
// removed, a rename, a password rotation on its own, swapping every role at
// once, and both data sources looked up by id and by name.
func TestOpenStackIdentitySecondLifecycle(t *testing.T) {
	mock := newMockIdentity()
	srv := newTestServer(t, mock.handler())
	t.Setenv("CLEURA_API_URL", srv)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)

	provider := `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}
`
	// Step 1: everything created disabled, with an explicit domain_id.
	step1 := provider + fmt.Sprintf(`
resource "cleura_openstack_project" "p" {
  domain_id = %[1]q
  name      = "second-project"
  enabled   = false
}

resource "cleura_openstack_user" "u" {
  domain_id           = %[1]q
  name                = "second-user"
  password            = "First-Passw0rd"
  password_wo_version = "1"
  enabled             = false
}
`, mock.domainID)

	// Step 2: enable both, add descriptions, rename the user, rotate the
	// password, and grant two roles.
	step2 := provider + fmt.Sprintf(`
resource "cleura_openstack_project" "p" {
  domain_id   = %[1]q
  name        = "second-project"
  description = "now described"
  enabled     = true
}

resource "cleura_openstack_user" "u" {
  domain_id           = %[1]q
  name                = "second-user-renamed"
  password            = "Second-Passw0rd"
  password_wo_version = "2"
  description         = "described too"
  enabled             = true
}

resource "cleura_openstack_role_assignment" "ra" {
  domain_id  = %[1]q
  user_id    = cleura_openstack_user.u.id
  project_id = cleura_openstack_project.p.id
  roles      = ["member", "swiftoperator"]
}

data "cleura_openstack_project" "by_id" {
  id = cleura_openstack_project.p.id
}

data "cleura_openstack_user" "by_name" {
  name       = cleura_openstack_user.u.name
  depends_on = [cleura_openstack_user.u]
}
`, mock.domainID)

	// Step 3: drop both descriptions and swap every role at once.
	step3 := provider + fmt.Sprintf(`
resource "cleura_openstack_project" "p" {
  domain_id = %[1]q
  name      = "second-project"
  enabled   = true
}

resource "cleura_openstack_user" "u" {
  domain_id           = %[1]q
  name                = "second-user-renamed"
  password            = "Second-Passw0rd"
  password_wo_version = "2"
  enabled             = true
}

resource "cleura_openstack_role_assignment" "ra" {
  domain_id  = %[1]q
  user_id    = cleura_openstack_user.u.id
  project_id = cleura_openstack_project.p.id
  roles      = ["load-balancer_member"]
}
`, mock.domainID)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: step1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cleura_openstack_project.p", "domain_id", mock.domainID),
					resource.TestCheckResourceAttr("cleura_openstack_project.p", "enabled", "false"),
					resource.TestCheckNoResourceAttr("cleura_openstack_project.p", "description"),
					resource.TestCheckResourceAttr("cleura_openstack_user.u", "enabled", "false"),
					resource.TestCheckNoResourceAttr("cleura_openstack_user.u", "description"),
					func(*terraform.State) error {
						if u := mock.userByName("second-user"); u == nil || u.Password != "First-Passw0rd" {
							return fmt.Errorf("user password not stored on create")
						}
						return nil
					},
				),
			},
			{
				Config: step2,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cleura_openstack_project.p", "enabled", "true"),
					resource.TestCheckResourceAttr("cleura_openstack_project.p", "description", "now described"),
					resource.TestCheckResourceAttr("cleura_openstack_user.u", "name", "second-user-renamed"),
					resource.TestCheckResourceAttr("cleura_openstack_user.u", "description", "described too"),
					resource.TestCheckResourceAttr("cleura_openstack_user.u", "enabled", "true"),
					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.ra", "roles.#", "2"),
					resource.TestCheckResourceAttrPair("data.cleura_openstack_project.by_id", "name", "cleura_openstack_project.p", "name"),
					resource.TestCheckResourceAttrPair("data.cleura_openstack_user.by_name", "id", "cleura_openstack_user.u", "id"),
					resource.TestCheckResourceAttr("data.cleura_openstack_user.by_name", "description", "described too"),
					func(*terraform.State) error {
						u := mock.userByName("second-user-renamed")
						if u == nil {
							return fmt.Errorf("renamed user not found")
						}
						if u.Password != "Second-Passw0rd" {
							return fmt.Errorf("password = %q, want the rotated one", u.Password)
						}
						return nil
					},
				),
			},
			{
				Config: step3,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("cleura_openstack_project.p", "description"),
					resource.TestCheckNoResourceAttr("cleura_openstack_user.u", "description"),
					resource.TestCheckResourceAttr("cleura_openstack_role_assignment.ra", "roles.#", "1"),
					resource.TestCheckTypeSetElemAttr("cleura_openstack_role_assignment.ra", "roles.*", "load-balancer_member"),
					func(*terraform.State) error {
						p := mock.projectByName("second-project")
						if p == nil {
							return fmt.Errorf("project missing")
						}
						got := mock.heldRoleNames("second-user-renamed", p.Id)
						if len(got) != 1 || got[0] != "load-balancer_member" {
							return fmt.Errorf("roles = %v, want [load-balancer_member]", got)
						}
						return nil
					},
				),
			},
		},
	})
}
