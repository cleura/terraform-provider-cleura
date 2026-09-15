package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestProviderCachesAreSingleFlight pins the property the whole OpenStack
// surface leans on: every resource resolves the region's domain and the
// domain's roles through ProviderConfig, so a graph walking many resources at
// once must still produce exactly one listing of each. Terraform's default
// parallelism is 10, and each of those resources calls these two helpers
// before doing anything else.
func TestProviderCachesAreSingleFlight(t *testing.T) {
	mock := newMockIdentity()
	cfg := newMockConfig(t, mock)

	const callers = 24
	var wg sync.WaitGroup
	ids := make([]string, callers)
	roleCounts := make([]int, callers)
	errs := make([]error, callers)
	start := make(chan struct{})

	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, so they contend for the caches
			id, err := cfg.regionDomainID(context.Background())
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = id
			roles, err := cfg.domainRoles(context.Background(), id)
			if err != nil {
				errs[i] = err
				return
			}
			roleCounts[i] = len(roles)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	for i := range callers {
		if ids[i] != mock.domainID {
			t.Errorf("caller %d resolved domain %q, want %q", i, ids[i], mock.domainID)
		}
		if roleCounts[i] != len(mockRoles) {
			t.Errorf("caller %d saw %d roles, want %d", i, roleCounts[i], len(mockRoles))
		}
	}

	mock.mu.Lock()
	domainCalls, roleCalls := mock.domainListCalls, mock.roleListCalls
	mock.mu.Unlock()
	if domainCalls != 1 {
		t.Errorf("%d concurrent callers produced %d domain listings, want exactly 1", callers, domainCalls)
	}
	if roleCalls != 1 {
		t.Errorf("%d concurrent callers produced %d role listings, want exactly 1", callers, roleCalls)
	}
}

// TestProviderCachesDoNotCacheFailures checks the other half of that contract:
// a failed lookup must not poison the cache, or a transient API error would
// wedge the provider for the rest of the run.
func TestProviderCachesDoNotCacheFailures(t *testing.T) {
	mock := newMockIdentity()
	cfg := newMockConfig(t, mock)

	mock.injectFaultOnce("GET /openstack/identity/v2/domains", 503)
	if _, err := cfg.regionDomainID(context.Background()); err == nil {
		t.Fatal("expected the injected 503 to fail domain resolution")
	}
	id, err := cfg.regionDomainID(context.Background())
	if err != nil {
		t.Fatalf("retry after a transient failure: %v", err)
	}
	if id != mock.domainID {
		t.Errorf("resolved domain %q, want %q", id, mock.domainID)
	}

	mock.injectFaultOnce("GET /openstack/identity/v2/domains/"+mock.domainID+"/roles", 503)
	if _, err := cfg.domainRoles(context.Background(), id); err == nil {
		t.Fatal("expected the injected 503 to fail the role listing")
	}
	roles, err := cfg.domainRoles(context.Background(), id)
	if err != nil {
		t.Fatalf("retry after a transient failure: %v", err)
	}
	if len(roles) != len(mockRoles) {
		t.Errorf("got %d roles, want %d", len(roles), len(mockRoles))
	}
}

// parallelIdentityConfig renders one project plus n users, each with its own
// role assignment. The users have no dependency on one another, so Terraform
// applies them concurrently up to its parallelism limit.
func parallelIdentityConfig(providerBlock, prefix string, n int) string {
	var b strings.Builder
	b.WriteString(providerBlock)
	fmt.Fprintf(&b, `
resource "cleura_openstack_project" "shared" {
  name = %q
}`, prefix+"-project")
	for i := range n {
		fmt.Fprintf(&b, `
resource "cleura_openstack_user" "u%[1]d" {
  name                = "%[2]s-user-%[1]d"
  password            = "Init1al-Passw0rd"
  password_wo_version = "1"
  description         = "parallel apply %[1]d"
}

resource "cleura_openstack_role_assignment" "u%[1]d" {
  user_id    = cleura_openstack_user.u%[1]d.id
  project_id = cleura_openstack_project.shared.id
  roles      = ["member"]
}`, i, prefix)
	}
	return b.String()
}

// TestOpenStackParallelApply runs a fan-out apply through the real Terraform
// binary: one project and twelve independent users, each with a role
// assignment, which exceeds Terraform's default parallelism of 10. It guards
// three things at once — that concurrent creates converge without error, that
// the result is idempotent (the framework fails the step on a non-empty plan
// afterwards), and that the provider's caches keep the lookup traffic flat
// rather than growing with the resource count.
func TestOpenStackParallelApply(t *testing.T) {
	const users = 12

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

	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttrSet("cleura_openstack_project.shared", "id"),
	}
	for i := range users {
		checks = append(checks,
			resource.TestCheckResourceAttr(fmt.Sprintf("cleura_openstack_user.u%d", i), "name", fmt.Sprintf("tfpar-user-%d", i)),
			resource.TestCheckResourceAttr(fmt.Sprintf("cleura_openstack_user.u%d", i), "description", fmt.Sprintf("parallel apply %d", i)),
			// The write-only password must never land in state, for any of them.
			resource.TestCheckNoResourceAttr(fmt.Sprintf("cleura_openstack_user.u%d", i), "password"),
			resource.TestCheckResourceAttr(fmt.Sprintf("cleura_openstack_role_assignment.u%d", i), "roles.#", "1"),
		)
	}
	// Every user must hold member on the shared project: a lost update in the
	// role-assignment path would show up here as a missing grant.
	checks = append(checks, func(*terraform.State) error {
		p := mock.projectByName("tfpar-project")
		if p == nil {
			return fmt.Errorf("project missing from the mock API")
		}
		for i := range users {
			name := fmt.Sprintf("tfpar-user-%d", i)
			if mock.userByName(name) == nil {
				return fmt.Errorf("user %q missing from the mock API", name)
			}
			// heldRoleNames keys on the user name, as the lifecycle test does.
			if got := mock.heldRoleNames(name, p.Id); len(got) != 1 || got[0] != "member" {
				return fmt.Errorf("user %q holds %v on the shared project, want [member]", name, got)
			}
		}
		return nil
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			if len(mock.users) != 0 {
				return fmt.Errorf("%d users survived destroy", len(mock.users))
			}
			for _, p := range mock.projects {
				if p.Enabled {
					return fmt.Errorf("project %q should be disabled after destroy", p.Name)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: parallelIdentityConfig(providerBlock, "tfpar", users),
			Check:  resource.ComposeAggregateTestCheckFunc(checks...),
		}},
	})

	// The counters cover the whole test: several Terraform commands (plan,
	// apply, the framework's post-apply plan and refresh, then destroy), each
	// of which configures a fresh provider instance and so re-primes the
	// caches once. The point of the bound is the shape, not the exact number:
	// with 25 resources in the graph, per-resource lookups would run into the
	// hundreds, while per-provider-instance lookups stay in the low tens.
	mock.mu.Lock()
	domainCalls, roleCalls := mock.domainListCalls, mock.roleListCalls
	mock.mu.Unlock()
	t.Logf("lookups across the run: %d domain listings, %d role listings (%d resources)", domainCalls, roleCalls, 1+2*users)
	if domainCalls > users {
		t.Errorf("%d domain listings for %d users: the domain cache is not holding", domainCalls, users)
	}
	if roleCalls > users {
		t.Errorf("%d role listings for %d users: the role cache is not holding", roleCalls, users)
	}
}
