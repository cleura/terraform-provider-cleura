package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/cleura/cleura-client-go/cleura"
)

func isNotFound(err error) bool { return errors.Is(err, errNotFound) }

// newTestServer starts a test server for handler and returns its URL.
func newTestServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// newMockConfig builds a ProviderConfig wired to mock, with the credentials
// the mock accepts.
func newMockConfig(t *testing.T, mock *mockIdentity) *ProviderConfig {
	t.Helper()
	client, err := cleura.NewClientWithCredentials(newTestServer(t, mock.handler()), mock.username, mock.token)
	if err != nil {
		t.Fatal(err)
	}
	return &ProviderConfig{Client: client, Cloud: "public", Region: "Sto2"}
}

// newTestConfig builds a ProviderConfig whose client talks to handler.
func newTestConfig(t *testing.T, handler http.Handler) *ProviderConfig {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := cleura.NewClientWithCredentials(srv.URL, "user", "token")
	if err != nil {
		t.Fatal(err)
	}
	return &ProviderConfig{Client: client, Cloud: "public", Region: "Sto2"}
}

// mockIdentity is an in-memory stand-in for the Cleura OpenStack identity API:
// one domain serving Sto2, plus projects and users that behave like the live
// endpoints (create/edit/list, no project GET or DELETE, user DELETE).
type mockIdentity struct {
	mu       sync.Mutex
	username string
	token    string
	domainID string
	nextID   int
	projects map[string]*api.OpenStackIdentityProject
	users    map[string]*mockUser
	// hidden projects exist but are left out of the caller's project listing,
	// simulating a project the API user has no access to.
	hidden map[string]bool
	// domainListCalls counts GET /domains, so a test can assert that an
	// explicit domain_id skips domain resolution entirely.
	domainListCalls int
	// roleListCalls counts GET /domains/{domain}/roles, so a test can assert
	// that the provider's per-domain role cache serves concurrent resources
	// from one listing instead of one per resource.
	roleListCalls int
	// bootstrapCalls counts POST .../bootstrap and bootstrapped records which
	// projects it was called for. The endpoint is write-only on the live API,
	// so a test can only observe it from this side.
	bootstrapCalls int
	bootstrapped   map[string]bool
	// fail injects faults keyed by "METHOD /path/prefix": a request whose method
	// and path prefix match is answered with that status and the API's error
	// envelope instead of reaching the handler.
	fail map[string]*mockFault
}

// mockFault is an injected failure, optionally limited to a number of uses so
// a test can model a transient error without breaking later cleanup calls.
type mockFault struct {
	status    int
	remaining int // 0 means unlimited
}

type mockUser struct {
	api.OpenStackIdentityUser
	Password string
	// grants: project id -> role id -> revoked-at (zero while held). Like the
	// live API, a revoked role stays listed for a moment (mockRevokeLag).
	grants map[string]map[string]time.Time
}

// mockRevokeLag imitates the few seconds a revoke takes to leave the live
// listing; kept short so the provider's settle loop is exercised, not the test's patience.
const mockRevokeLag = 1500 * time.Millisecond

var mockRoles = []api.OpenStackIdentityProjectRole{
	{Id: "6f534bf872ee4655bf168ab98817d54e", Name: "member"},
	{Id: "37071fc082e14c2284c32a2761f71c63", Name: "swiftoperator"},
	{Id: "1b54373ebea24df9ae903219116281a2", Name: "load-balancer_member"},
}

func mockRoleByID(id string) (api.OpenStackIdentityProjectRole, bool) {
	for _, r := range mockRoles {
		if r.Id == id {
			return r, true
		}
	}
	return api.OpenStackIdentityProjectRole{}, false
}

// heldRoles lists the roles a user currently appears to hold on a project,
// including revoked ones still inside the lag window.
func (u *mockUser) heldRoles(project string) []api.OpenStackIdentityProjectRole {
	var out []api.OpenStackIdentityProjectRole
	for id, revokedAt := range u.grants[project] {
		if revokedAt.IsZero() || time.Since(revokedAt) < mockRevokeLag {
			r, _ := mockRoleByID(id)
			out = append(out, r)
		}
	}
	return out
}

func newMockIdentity() *mockIdentity {
	return &mockIdentity{
		username: "acc-user",
		token:    "acc-token",
		domainID: "0123456789abcdef0123456789abcdef",
		projects: map[string]*api.OpenStackIdentityProject{},
		users:    map[string]*mockUser{},
		hidden:   map[string]bool{},
		fail:     map[string]*mockFault{},
	}
}

// injectFault makes every "METHOD /path-prefix" request fail with status until
// clearFaults is called.
func (m *mockIdentity) injectFault(route string, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail[route] = &mockFault{status: status}
}

// injectFaultOnce fails only the next matching request, like a transient error.
func (m *mockIdentity) injectFaultOnce(route string, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail[route] = &mockFault{status: status, remaining: 1}
}

func (m *mockIdentity) clearFaults() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = map[string]*mockFault{}
}

// faultFor reports an injected status for this request, if any, consuming one
// use of a limited fault.
func (m *mockIdentity) faultFor(method, path string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for route, f := range m.fail {
		parts := strings.SplitN(route, " ", 2)
		if len(parts) != 2 || parts[0] != method || !strings.HasPrefix(path, parts[1]) {
			continue
		}
		if f.remaining > 0 {
			f.remaining--
			if f.remaining == 0 {
				delete(m.fail, route)
			}
		}
		return f.status, true
	}
	return 0, false
}

func (m *mockIdentity) newID() string {
	m.nextID++
	return fmt.Sprintf("%032x", m.nextID)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.FrameworkHttpErrorResponse{Error: api.FrameworkHttpErrorContent{Code: status, Message: msg}})
}

func (m *mockIdentity) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openstack/identity/v2/domains", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.domainListCalls++
		m.mu.Unlock()
		name := "CCP_Domain_1"
		writeJSON(w, 200, []api.CommonOpenStackDomain{{
			Id: m.domainID, Name: &name, Status: "active",
			Area: api.CommonOpenStackDomainArea{Id: 1, Name: "Stockholm", Tag: "sto", Regions: []api.CommonOpenStackRegion{{Id: 1, Name: "Stockholm 2", Tag: "Sto2"}}},
		}})
	})
	mux.HandleFunc("GET /openstack/identity/v1/current-user/projects", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		region := api.OpenStackIdentityRegionWithProjects{Region: api.OpenStackIdentityRegion{Id: 1, Name: "Stockholm 2", Tag: "Sto2", NetworkProvider: "ovn", ObjectStorageEnabled: true}}
		for _, p := range m.projects {
			if !m.hidden[p.Id] {
				region.Projects = append(region.Projects, *p)
			}
		}
		writeJSON(w, 200, []api.OpenStackIdentityRegionWithProjects{region})
	})
	mux.HandleFunc("POST /openstack/identity/v2/domains/{domain}/projects", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		var body api.OpenStackIdentityCreateProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			apiError(w, 400, "Bad Request")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, p := range m.projects {
			if p.Name == body.Name {
				apiError(w, 400, "Bad Request: project name already in use")
				return
			}
		}
		p := &api.OpenStackIdentityProject{Id: m.newID(), Name: body.Name, DomainId: m.domainID, Enabled: true, Description: body.Description}
		m.projects[p.Id] = p
		writeJSON(w, 201, p)
	})
	mux.HandleFunc("PATCH /openstack/identity/v2/domains/{domain}/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		var body api.OpenStackIdentityEditProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiError(w, 400, "Bad Request")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		p, ok := m.projects[r.PathValue("project")]
		if !ok {
			apiError(w, 404, "Not Found")
			return
		}
		if body.Name != nil {
			p.Name = *body.Name
		}
		if body.Description != nil {
			// The live API reports a cleared description as null.
			if *body.Description == "" {
				p.Description = nil
			} else {
				p.Description = body.Description
			}
		}
		if body.Enabled != nil {
			p.Enabled = *body.Enabled
		}
		writeJSON(w, 200, p)
	})
	// Verified live: the quota endpoint is the one per-project GET that 404s
	// for an unknown project.
	mux.HandleFunc("GET /openstack/identity/v2/domains/{domain}/projects/{project}/quotas/{region}", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.projects[r.PathValue("project")]; !ok {
			apiError(w, 404, "Not Found: Project not found")
			return
		}
		writeJSON(w, 200, api.OpenStackIdentityProjectQuota{})
	})
	mux.HandleFunc("GET /openstack/identity/v2/domains/{domain}/users", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		users := []api.OpenStackIdentityUser{}
		for _, u := range m.users {
			users = append(users, u.OpenStackIdentityUser)
		}
		writeJSON(w, 200, users)
	})
	mux.HandleFunc("POST /openstack/identity/v2/domains/{domain}/users", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		var body api.OpenStackIdentityCreateUserRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Password == "" {
			apiError(w, 400, "Bad Request")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		// Like the live API, create ignores the description (PATCH stores it).
		empty := ""
		u := &mockUser{OpenStackIdentityUser: api.OpenStackIdentityUser{Id: m.newID(), Name: body.Name, DomainId: m.domainID, Enabled: true, Description: &empty}, Password: body.Password, grants: map[string]map[string]time.Time{}}
		m.users[u.Id] = u
		writeJSON(w, 201, api.OpenStackIdentityUserWithProjectsAccess{Id: u.Id, Name: u.Name, DomainId: u.DomainId, Enabled: u.Enabled, Description: u.Description})
	})
	mux.HandleFunc("PATCH /openstack/identity/v2/domains/{domain}/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		var body api.OpenStackIdentityEditUserRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiError(w, 400, "Bad Request")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		u, ok := m.users[r.PathValue("user")]
		if !ok {
			apiError(w, 404, "Not Found")
			return
		}
		if body.Name != nil {
			u.Name = *body.Name
		}
		if body.Description != nil {
			if *body.Description == "" {
				u.Description = nil
			} else {
				u.Description = body.Description
			}
		}
		if body.Enabled != nil {
			u.Enabled = *body.Enabled
		}
		if body.Password != nil {
			u.Password = *body.Password
		}
		writeJSON(w, 200, api.OpenStackIdentityUserWithProjectsAccess{Id: u.Id, Name: u.Name, DomainId: u.DomainId, Enabled: u.Enabled, Description: u.Description})
	})
	mux.HandleFunc("DELETE /openstack/identity/v2/domains/{domain}/users/{user}", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.users[r.PathValue("user")]; !ok {
			apiError(w, 404, "Not Found")
			return
		}
		delete(m.users, r.PathValue("user"))
		w.WriteHeader(204)
	})

	// Gardener bootstrap. Only POST exists live: GET and DELETE on this path
	// answer 404, and an unknown project id answers 500 rather than 404.
	mux.HandleFunc("POST /gardener/v2/{cloud}/{region}/{project}/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.bootstrapCalls++
		project := r.PathValue("project")
		if _, ok := m.projects[project]; !ok {
			apiError(w, 500, "Internal Server Error")
			return
		}
		if m.bootstrapped == nil {
			m.bootstrapped = map[string]bool{}
		}
		m.bootstrapped[project] = true
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /openstack/identity/v2/domains/{domain}/roles", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		m.mu.Lock()
		m.roleListCalls++
		m.mu.Unlock()
		writeJSON(w, 200, mockRoles)
	})
	mux.HandleFunc("POST /openstack/identity/v2/domains/{domain}/users/{user}/project-accesses", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		var body api.OpenStackIdentityGrantProjectAccessRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiError(w, 400, "Bad Request")
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		u, ok := m.users[r.PathValue("user")]
		if !ok {
			apiError(w, 404, "Not Found: No such user")
			return
		}
		for _, p := range body.Projects {
			if _, ok := m.projects[p.ProjectId]; !ok {
				apiError(w, 404, fmt.Sprintf("Invalid project: Project with id %s could not be found.", p.ProjectId))
				return
			}
			for _, roleID := range p.Roles {
				if _, ok := mockRoleByID(roleID); !ok {
					apiError(w, 404, "Invalid role: Could not find role "+roleID)
					return
				}
				if u.grants[p.ProjectId] == nil {
					u.grants[p.ProjectId] = map[string]time.Time{}
				}
				u.grants[p.ProjectId][roleID] = time.Time{}
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /openstack/identity/v2/domains/{domain}/users/{user}/projects", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		memberships := []api.OpenStackIdentityProjectMembership{}
		// Like the live API, an unknown user yields an empty list, not a 404.
		if u, ok := m.users[r.PathValue("user")]; ok {
			for projectID := range u.grants {
				roles := u.heldRoles(projectID)
				if len(roles) == 0 {
					continue
				}
				p := m.projects[projectID]
				memberships = append(memberships, api.OpenStackIdentityProjectMembership{Id: p.Id, Name: p.Name, DomainId: p.DomainId, Roles: &roles})
			}
		}
		writeJSON(w, 200, memberships)
	})
	mux.HandleFunc("DELETE /openstack/identity/v2/domains/{domain}/users/{user}/projects/{project}/roles/{role}", func(w http.ResponseWriter, r *http.Request) {
		if !m.checkDomain(w, r) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		u, ok := m.users[r.PathValue("user")]
		if !ok {
			apiError(w, 404, "Not Found: No such user")
			return
		}
		revokedAt, held := u.grants[r.PathValue("project")][r.PathValue("role")]
		if !held || !revokedAt.IsZero() {
			// Verified live: revoking an unassigned role is an opaque 500.
			apiError(w, 500, "OpenStack error occurred.")
			return
		}
		u.grants[r.PathValue("project")][r.PathValue("role")] = time.Now()
		w.WriteHeader(204)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-AUTH-LOGIN") != m.username || r.Header.Get("X-AUTH-TOKEN") != m.token {
			apiError(w, 403, "Invalid token")
			return
		}
		if status, ok := m.faultFor(r.Method, r.URL.Path); ok {
			apiError(w, status, "Injected fault")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (m *mockIdentity) checkDomain(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("domain") != m.domainID {
		apiError(w, 404, "Not Found: unknown domain")
		return false
	}
	return true
}

// projectByName returns the mock's project with that name, if any.
func (m *mockIdentity) projectByName(name string) *api.OpenStackIdentityProject {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.projects {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// userByName returns the mock's user with that name, if any.
func (m *mockIdentity) userByName(name string) *mockUser {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Name == name {
			return u
		}
	}
	return nil
}

// addProject seeds a project that already exists in the account, for tests
// that need one without applying a cleura_openstack_project first. It returns
// the new project's id.
func (m *mockIdentity) addProject(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := &api.OpenStackIdentityProject{Id: m.newID(), Name: name, DomainId: m.domainID, Enabled: true}
	m.projects[p.Id] = p
	return p.Id
}

// heldRoleNames returns the role names a user currently holds on a project
// (revoked roles past the lag window excluded).
func (m *mockIdentity) heldRoleNames(user, project string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Name != user {
			continue
		}
		var names []string
		for _, r := range u.heldRoles(project) {
			names = append(names, r.Name)
		}
		return names
	}
	return nil
}
