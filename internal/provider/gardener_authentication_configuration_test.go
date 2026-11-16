package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// mockAuthConfigAPI is an in-memory Gardener IdentityProvider API that behaves
// the way the dev API was measured to (2026-10-05): the server sets prefix to
// oidc:<alias>: for claim mappings and to none for groups mapped by expression,
// audience_match_policy defaults to MatchAny, issuer order is kept as sent, a
// duplicate name is 409, and deleting a configuration in use is 409.
type mockAuthConfigAPI struct {
	mu      sync.Mutex
	configs map[string]api.GardenerIdentityProviderAuthenticationConfiguration
	inUse   map[string]bool
	bodies  []map[string]any // decoded request bodies of POST and PUT, in order
	calls   []string         // "PUT <name>" and "RECONCILE <name>", in order
	// reconciled is what the reconcile endpoint reports per cluster.
	reconciled []api.GardenerIdentityProviderShootReconcileResult
}

func newMockAuthConfigAPI() *mockAuthConfigAPI {
	return &mockAuthConfigAPI{configs: map[string]api.GardenerIdentityProviderAuthenticationConfiguration{}, inUse: map[string]bool{}}
}

func (m *mockAuthConfigAPI) store(name string, issuers []api.GardenerIdentityProviderIssuer) api.GardenerIdentityProviderAuthenticationConfiguration {
	for i := range issuers {
		iss := &issuers[i]
		p := "oidc:" + iss.Alias + ":"
		iss.ClaimMappings.Username.Prefix = &p
		if u := iss.ClaimMappings.Uid; u != nil {
			u.Prefix = &p
		}
		if g := iss.ClaimMappings.Groups; g != nil {
			g.Prefix = nil
			if g.Claim != nil {
				g.Prefix = &p
			}
		}
		if iss.AudienceMatchPolicy == nil {
			policy := api.MatchAny
			iss.AudienceMatchPolicy = &policy
		}
	}
	c := api.GardenerIdentityProviderAuthenticationConfiguration{Name: name, Issuers: issuers}
	m.configs[name] = c
	return c
}

func (m *mockAuthConfigAPI) record(r *http.Request) []byte {
	raw, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	m.bodies = append(m.bodies, decoded)
	return raw
}

func (m *mockAuthConfigAPI) handler() http.Handler {
	const root = "/gardener/v2/public/authentication-configurations"
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+root, func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var body api.GardenerIdentityProviderCreateAuthenticationConfiguration
		_ = json.Unmarshal(m.record(r), &body)
		if _, exists := m.configs[body.Name]; exists {
			apiError(w, 409, fmt.Sprintf("An authentication configuration with name '%s' already exists.", body.Name))
			return
		}
		writeJSON(w, 201, m.store(body.Name, body.Issuers))
	})
	mux.HandleFunc("GET "+root+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		c, ok := m.configs[r.PathValue("name")]
		if !ok {
			apiError(w, 404, fmt.Sprintf("An authentication configuration with name '%s' does not exist.", r.PathValue("name")))
			return
		}
		writeJSON(w, 200, c)
	})
	mux.HandleFunc("PUT "+root+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		var body api.GardenerIdentityProviderUpdateAuthenticationConfiguration
		_ = json.Unmarshal(m.record(r), &body)
		if _, ok := m.configs[r.PathValue("name")]; !ok {
			apiError(w, 404, "not found")
			return
		}
		m.calls = append(m.calls, "PUT "+r.PathValue("name"))
		writeJSON(w, 202, m.store(r.PathValue("name"), body.Issuers))
	})
	mux.HandleFunc("POST "+root+"/{name}/shoots/reconcile", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.calls = append(m.calls, "RECONCILE "+r.PathValue("name"))
		writeJSON(w, 202, append([]api.GardenerIdentityProviderShootReconcileResult{}, m.reconciled...))
	})
	mux.HandleFunc("DELETE "+root+"/{name}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		name := r.PathValue("name")
		if m.inUse[name] {
			apiError(w, 409, "The authentication configuration is in use.")
			return
		}
		if _, ok := m.configs[name]; !ok {
			apiError(w, 404, "not found")
			return
		}
		delete(m.configs, name)
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET "+root+"/{name}/shoots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []api.GardenerIdentityProviderShootReference{
			{Name: "prod", OpenstackProjectId: "p1", OpenstackRegionTag: "Sto2"},
		})
	})
	return mux
}

// callsAre checks the PUT and reconcile calls made so far, in order.
func (m *mockAuthConfigAPI) callsAre(want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if got := strings.Join(m.calls, ", "); got != strings.Join(want, ", ") {
			return fmt.Errorf("calls: [%s]; want [%s]", got, strings.Join(want, ", "))
		}
		return nil
	}
}

func (m *mockAuthConfigAPI) lastBody(t *testing.T) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		t.Fatal("no request body recorded")
	}
	return m.bodies[len(m.bodies)-1]
}

func serveAuthConfigAPI(t *testing.T, m *mockAuthConfigAPI) {
	t.Helper()
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", "user")
	t.Setenv("CLEURA_API_TOKEN", "token")
}

// testCertificatePEM returns a fresh self-signed certificate in PEM form.
func testCertificatePEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

const authConfigProvider = `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}
`

// authConfigHCL renders a configuration with two issuers. zetaGroups is the
// body of zeta's groups mapping.
func authConfigHCL(ca, zetaGroups string) string {
	return authConfigProvider + `
resource "cleura_gardener_authentication_configuration" "test" {
  name = "corp-sso"
  issuers = {
    zeta = {
      url       = "https://idp.example.com/realms/zeta"
      audiences = ["kubectl", "ci"]
      certificate_authority = <<EOT
` + ca + `EOT
      claim_mappings = {
        username = { claim = "email" }
        groups   = { ` + zetaGroups + ` }
      }
      user_validation_rules = [
        { expression = "!user.username.startsWith('system:')", message = "no system: users" },
      ]
    }
    alpha = {
      url       = "https://login.example.org/alpha"
      audiences = ["kube-api"]
      claim_mappings = {
        username = { claim = "sub" }
        uid      = { claim = "sub" }
      }
    }
  }
}
`
}

func TestAuthenticationConfigurationLifecycle(t *testing.T) {
	mock := newMockAuthConfigAPI()
	serveAuthConfigAPI(t, mock)
	ca := testCertificatePEM(t)
	const addr = "cleura_gardener_authentication_configuration.test"
	zetaGroups := tfjsonpath.New("issuers").AtMapKey("zeta").AtMapKey("claim_mappings").AtMapKey("groups")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: authConfigHCL(ca, `claim = "groups"`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.StringExact("corp-sso")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("issuers").AtMapKey("zeta").AtMapKey("audience_match_policy"), knownvalue.StringExact("MatchAny")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("issuers").AtMapKey("zeta").AtMapKey("certificate_authority"), knownvalue.StringExact(ca)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("issuers").AtMapKey("zeta").AtMapKey("claim_mappings").AtMapKey("username").AtMapKey("prefix"), knownvalue.StringExact("oidc:zeta:")),
					statecheck.ExpectKnownValue(addr, zetaGroups.AtMapKey("prefix"), knownvalue.StringExact("oidc:zeta:")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("issuers").AtMapKey("alpha").AtMapKey("claim_mappings").AtMapKey("uid").AtMapKey("prefix"), knownvalue.StringExact("oidc:alpha:")),
				},
				Check: func(*terraform.State) error { return checkCreateBody(t, mock.lastBody(t), ca) },
			},
			{
				// Unchanged configuration: nothing to do, so reads match what was written.
				Config:           authConfigHCL(ca, `claim = "groups"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// Switching groups from a claim to an expression drops the prefix; the
				// plan must say so instead of carrying the claim's prefix forward.
				Config: authConfigHCL(ca, `expression = "claims.roles"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue(addr, zetaGroups.AtMapKey("prefix"), knownvalue.Null()),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, zetaGroups.AtMapKey("expression"), knownvalue.StringExact("claims.roles")),
				},
				// reconcile_on_update defaults to false: the update touches no cluster.
				Check: mock.callsAre("PUT corp-sso"),
			},
			{
				// And back: the prefix is unknown until the API reports it.
				Config: authConfigHCL(ca, `claim = "groups"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectUnknownValue(addr, zetaGroups.AtMapKey("prefix")),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, zetaGroups.AtMapKey("prefix"), knownvalue.StringExact("oidc:zeta:")),
				},
			},
			{
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateId:                        "corp-sso",
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
		},
	})
}

// checkCreateBody asserts what was sent on create: issuers sorted by alias,
// no server-set prefix, the default policy, and the CA as base64 of the PEM.
func checkCreateBody(t *testing.T, body map[string]any, ca string) error {
	issuers, _ := body["issuers"].([]any)
	if len(issuers) != 2 {
		return fmt.Errorf("sent %d issuers, want 2", len(issuers))
	}
	first, _ := issuers[0].(map[string]any)
	second, _ := issuers[1].(map[string]any)
	if first["alias"] != "alpha" || second["alias"] != "zeta" {
		return fmt.Errorf("issuers sent as %v, %v; want sorted alpha, zeta", first["alias"], second["alias"])
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), `"prefix"`) {
		return fmt.Errorf("request carries a server-set prefix: %s", raw)
	}
	if second["certificate_authority"] != base64.StdEncoding.EncodeToString([]byte(ca)) {
		return fmt.Errorf("certificate_authority was not sent as base64 of the configured PEM")
	}
	if first["certificate_authority"] != nil {
		return fmt.Errorf("alpha has no CA configured but one was sent: %v", first["certificate_authority"])
	}
	if second["audience_match_policy"] != "MatchAny" {
		return fmt.Errorf("audience_match_policy sent as %v, want MatchAny", second["audience_match_policy"])
	}
	return nil
}

func TestAuthenticationConfigurationPlanTimeValidation(t *testing.T) {
	serveAuthConfigAPI(t, newMockAuthConfigAPI())
	issuer := func(alias, body string) string {
		return authConfigProvider + fmt.Sprintf(`
resource "cleura_gardener_authentication_configuration" "test" {
  name    = "corp-sso"
  issuers = { %s = { %s } }
}`, alias, body)
	}
	const ok = `url = "https://idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" } }`
	cases := []struct {
		name, config, want string
	}{
		{"http URL", issuer("probe", `url = "http://idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" } }`), "must use https"},
		{"URL with a query", issuer("probe", `url = "https://idp.example.com/r?x=1", audiences = ["a"], claim_mappings = { username = { claim = "email" } }`), "must not contain a query"},
		{"URL with credentials", issuer("probe", `url = "https://u:p@idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" } }`), "must not contain a username or password"},
		{"URL with a fragment", issuer("probe", `url = "https://idp.example.com/r#x", audiences = ["a"], claim_mappings = { username = { claim = "email" } }`), "must not contain a fragment"},
		{"alias too short", issuer("ab", ok), "string length must be between 3 and 32"},
		{"alias with invalid characters", issuer("Bad_Alias", ok), `lowercase letters, digits\s+and hyphens`},
		{"name with invalid characters", strings.Replace(issuer("probe", ok), `"corp-sso"`, `"Corp_SSO"`, 1), `lowercase letters, digits\s+and hyphens`},
		{"MatchExact", issuer("probe", ok+`, audience_match_policy = "MatchExact"`), `value must be one of`},
		{"duplicate audiences", issuer("probe", `url = "https://idp.example.com/r", audiences = ["a", "a"], claim_mappings = { username = { claim = "email" } }`), "duplicate"},
		{"no audiences", issuer("probe", `url = "https://idp.example.com/r", audiences = [], claim_mappings = { username = { claim = "email" } }`), "at least 1"},
		{"groups with neither claim nor expression", issuer("probe", `url = "https://idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" }, groups = {} }`), `when one \(and only one\) of`},
		{"groups with both", issuer("probe", `url = "https://idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" }, groups = { claim = "g", expression = "claims.g" } }`), `when one \(and only one\) of`},
		{"empty extra list", issuer("probe", `url = "https://idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" }, extra = [] }`), "at least 1"},
		{"CA already base64-encoded", issuer("probe", ok+`, certificate_authority = "`+base64.StdEncoding.EncodeToString([]byte("x"))+`"`), "Do not base64-encode it yourself"},
		{"duplicate issuer URL", authConfigProvider + `
resource "cleura_gardener_authentication_configuration" "test" {
  name = "corp-sso"
  issuers = {
    one = { url = "https://idp.example.com/same", audiences = ["a"], claim_mappings = { username = { claim = "email" } } }
    two = { url = "https://idp.example.com/same", audiences = ["b"], claim_mappings = { username = { claim = "email" } } }
  }
}`, "Duplicate issuer URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{{Config: tc.config, PlanOnly: true, ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
}

func TestAuthenticationConfigurationConflicts(t *testing.T) {
	mock := newMockAuthConfigAPI()
	serveAuthConfigAPI(t, mock)
	const config = authConfigProvider + `
resource "cleura_gardener_authentication_configuration" "test" {
  name    = "corp-sso"
  issuers = { probe = { url = "https://idp.example.com/r", audiences = ["a"], claim_mappings = { username = { claim = "email" } } } }
}`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// A configuration with the same name exists outside Terraform.
				PreConfig: func() {
					mock.mu.Lock()
					mock.store("corp-sso", []api.GardenerIdentityProviderIssuer{{Alias: "x", Url: "https://x.example.com", Audiences: []string{"a"}}})
					mock.mu.Unlock()
				},
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)already exists.*import it with the ID\s+"corp-sso"`),
			},
			{
				PreConfig: func() { mock.mu.Lock(); delete(mock.configs, "corp-sso"); mock.mu.Unlock() },
				Config:    config,
			},
			{
				// Destroying a configuration that a cluster uses names the cluster.
				PreConfig:   func() { mock.mu.Lock(); mock.inUse["corp-sso"] = true; mock.mu.Unlock() },
				Config:      config,
				Destroy:     true,
				ExpectError: regexp.MustCompile(`(?s)It is used by:\s+prod\s+\(project\s+p1,\s+region\s+Sto2\)`),
			},
			{
				PreConfig: func() { mock.mu.Lock(); mock.inUse["corp-sso"] = false; mock.mu.Unlock() },
				Config:    config,
			},
		},
	})
}

func TestIssuerURLProblem(t *testing.T) {
	for raw, want := range map[string]string{
		"https://keycloak.example.com/realms/Gardener%20OIDC": "",
		"https://login.microsoftonline.com/t/v2.0":            "",
		"http://idp.example.com":                              "must use https",
		"https://idp.example.com/?":                           "must not contain a query",
		"https://":                                            "has no host",
	} {
		got := issuerURLProblem(raw)
		if (want == "" && got != "") || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("issuerURLProblem(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestReconcileShootsReportsMissedClusters: clusters that could not be asked
// to apply an update are listed in one warning, never an error, because the
// configuration itself was updated and another apply would have nothing to do.
func TestReconcileShootsReportsMissedClusters(t *testing.T) {
	mock := newMockAuthConfigAPI()
	mock.reconciled = []api.GardenerIdentityProviderShootReconcileResult{
		{Name: "prod", OpenstackProjectId: "p1", OpenstackRegionTag: "Sto2", Status: api.Accepted},
		{Name: "busy", OpenstackProjectId: "p1", OpenstackRegionTag: "Sto2", Status: api.SkippedOperationInProgress},
		{Name: "broken", OpenstackProjectId: "p2", OpenstackRegionTag: "Fra1", Status: api.Failed},
	}
	r := &authenticationConfigurationResource{config: newTestConfig(t, mock.handler())}

	diags := r.reconcileShoots(context.Background(), "corp-sso")
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v; want exactly one warning", diags)
	}
	detail := diags.Warnings()[0].Detail()
	t.Logf("rendered warning:\n%s", detail)
	for _, want := range []string{
		`"corp-sso" was updated and 1 of its 3 clusters was reconciled. These 2 still use the previous version`,
		"Busy with another operation:\n    Sto2, project p1: busy",
		`Could not be reconciled (status "failed"):` + "\n    Fra1, project p2: broken",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("warning lacks %q", want)
		}
	}
	if strings.Contains(detail, "prod") {
		t.Errorf("warning names the reconciled cluster prod")
	}

	// A failed request is a warning too.
	failing := &authenticationConfigurationResource{config: newTestConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 500, "Internal Server Error")
	}))}
	diags = failing.reconcileShoots(context.Background(), "corp-sso")
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Errorf("failed request: diagnostics = %v; want exactly one warning", diags)
	}
}

// TestFormatClusters: one line per region and project, sorted, and capped so a
// configuration shared by many clusters still gives a readable warning.
func TestFormatClusters(t *testing.T) {
	var clusters []clusterRef
	for i := 25; i >= 1; i-- {
		region, project := "Kna1", "p-kna"
		if i%3 == 0 {
			region, project = "Sto2", "p-sto"
		}
		clusters = append(clusters, clusterRef{fmt.Sprintf("c%02d", i), project, region})
	}
	got := formatClusters(clusters)
	t.Logf("rendered:\n%s", got)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "  Kna1, project p-kna: c01, c02, c04") ||
		!strings.HasPrefix(lines[1], "  Sto2, project p-sto: ") || lines[2] != "  … and 5 more" {
		t.Errorf("formatClusters =\n%s", got)
	}
	if n := len(regexp.MustCompile(`\bc\d\d\b`).FindAllString(got, -1)); n != 20 {
		t.Errorf("listed %d clusters; want 20", n)
	}
}

// TestAuthenticationConfigurationReconcileOnUpdate: with reconcile_on_update,
// an update is followed by the reconcile that rolls it out to the clusters.
// Create has no clusters to reconcile, and changing only the attribute
// contacts nothing.
func TestAuthenticationConfigurationReconcileOnUpdate(t *testing.T) {
	mock := newMockAuthConfigAPI()
	serveAuthConfigAPI(t, mock)
	ca := testCertificatePEM(t)
	const addr = "cleura_gardener_authentication_configuration.test"
	withReconcile := func(on bool, zetaGroups string) string {
		return strings.Replace(authConfigHCL(ca, zetaGroups), `name = "corp-sso"`,
			fmt.Sprintf("name = \"corp-sso\"\n  reconcile_on_update = %t", on), 1)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withReconcile(true, `claim = "groups"`),
				Check:  mock.callsAre(),
			},
			{
				Config: withReconcile(true, `expression = "claims.roles"`),
				Check:  mock.callsAre("PUT corp-sso", "RECONCILE corp-sso"),
			},
			{
				// Only the attribute changes: an update with no API call.
				Config: withReconcile(false, `expression = "claims.roles"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					mock.callsAre("PUT corp-sso", "RECONCILE corp-sso"),
					resource.TestCheckResourceAttr(addr, "reconcile_on_update", "false"),
				),
			},
		},
	})
}
