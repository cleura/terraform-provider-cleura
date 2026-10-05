package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// oidcShootConfig is the identity test's shoot, with an optional
// authentication_configuration_name line and extra HCL appended.
func oidcShootConfig(authLine, extra string) string {
	base := identityTestConfig("idtest")
	base = strings.Replace(base, `  kubernetes_version = "1.35.6"`, `  kubernetes_version = "1.35.6"`+"\n  "+authLine, 1)
	return base + "\n" + extra
}

// lastPatch returns the most recent PATCH body as raw JSON fields.
func (m *mockShootAPI) lastPatch(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.patches) == 0 {
		t.Fatal("no PATCH was sent")
	}
	return m.patches[len(m.patches)-1]
}

func TestShootAuthenticationConfigurationAttachSwitchDetach(t *testing.T) {
	mock := &mockShootAPI{}
	serveShootAPI(t, mock)
	const addr = "cleura_gardener_shoot.test"
	auth := tfjsonpath.New("authentication_configuration_name")

	patchSent := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			v, present := mock.lastPatch(t)["authentication_configuration_name"]
			switch {
			case want == "<absent>" && present:
				return fmt.Errorf("PATCH carried authentication_configuration_name=%s, want it left out", v)
			case want == "<absent>":
				return nil
			case !present:
				return fmt.Errorf("PATCH left out authentication_configuration_name, want %s", want)
			case string(v) != want:
				return fmt.Errorf("PATCH sent authentication_configuration_name=%s, want %s", v, want)
			}
			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Attached on create.
				Config:            oidcShootConfig(`authentication_configuration_name = "corp-sso"`, ""),
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(addr, auth, knownvalue.StringExact("corp-sso"))},
			},
			{
				Config:           oidcShootConfig(`authentication_configuration_name = "corp-sso"`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// Switched in place.
				Config: oidcShootConfig(`authentication_configuration_name = "other-sso"`, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(addr, auth, knownvalue.StringExact("other-sso"))},
				Check:             patchSent(`"other-sso"`),
			},
			{
				// Removed: the PATCH must carry an explicit null, since a missing
				// field means "unchanged" to the API.
				Config:            oidcShootConfig("", ""),
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(addr, auth, knownvalue.Null())},
				Check:             patchSent(`null`),
			},
			{
				Config:           oidcShootConfig("", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
		},
	})
}

func TestEditShootRequestBody(t *testing.T) {
	name := "corp-sso"
	for _, tc := range []struct {
		label  string
		body   api.GardenerEditShootJSONRequestBody
		detach bool
		want   string // the JSON of authentication_configuration_name, or "<absent>"
	}{
		{"unchanged is left out", api.GardenerEditShootJSONRequestBody{}, false, "<absent>"},
		{"a change is sent", api.GardenerEditShootJSONRequestBody{AuthenticationConfigurationName: &name}, false, `"corp-sso"`},
		{"a removal is an explicit null", api.GardenerEditShootJSONRequestBody{}, true, "null"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			encoded, err := editShootRequestBody(tc.body, tc.detach)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			v, present := fields["authentication_configuration_name"]
			got := "<absent>"
			if present {
				got = string(v)
			}
			if got != tc.want {
				t.Errorf("authentication_configuration_name = %s, want %s (body %s)", got, tc.want, encoded)
			}
		})
	}
}

func TestShootOIDCKubeconfigDataSource(t *testing.T) {
	mock := &mockShootAPI{issuers: map[string][]api.GardenerIdentityProviderIssuerReference{
		"corp-sso": {{Alias: "keycloak", Url: "https://idp.example.com/realms/x", Audiences: []api.GardenerIdentityProviderAudienceReference{
			{Name: "kube-api", Key: "aaaa11112222"},
			{Name: "ci", Key: "bbbb33334444"},
		}}},
	}}
	serveShootAPI(t, mock)
	dataSource := func(alias, audience string) string {
		return fmt.Sprintf(`
data "cleura_gardener_shoot_oidc_kubeconfig" "ci" {
  shoot_name   = cleura_gardener_shoot.test.name
  issuer_alias = %q
  audience     = %q
}`, alias, audience)
	}
	const ds = "data.cleura_gardener_shoot_oidc_kubeconfig.ci"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The audience name resolves to its key; the second audience is chosen.
				Config: oidcShootConfig(`authentication_configuration_name = "corp-sso"`, dataSource("keycloak", "ci")),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(ds, tfjsonpath.New("kubeconfig"), knownvalue.StringExact("kubeconfig for keycloak/bbbb33334444")),
					statecheck.ExpectKnownValue(ds, tfjsonpath.New("authentication_configuration_name"), knownvalue.StringExact("corp-sso")),
					statecheck.ExpectKnownValue(ds, tfjsonpath.New("id"), knownvalue.StringExact(identityTestProject+"/idtest/keycloak/ci")),
				},
			},
			{
				Config:      oidcShootConfig(`authentication_configuration_name = "corp-sso"`, dataSource("keycloak", "nope")),
				ExpectError: regexp.MustCompile(`(?s)has\s+no\s+audience\s+"nope".*Its\s+audiences\s+are:\s+ci,\s+kube-api`),
			},
			{
				Config:      oidcShootConfig(`authentication_configuration_name = "corp-sso"`, dataSource("entra", "ci")),
				ExpectError: regexp.MustCompile(`(?s)has\s+no\s+issuer\s+"entra".*Its\s+issuers\s+are:\s+keycloak`),
			},
			{
				// Without a configuration on the cluster there is nothing to fetch.
				Config:      oidcShootConfig("", dataSource("keycloak", "ci")),
				ExpectError: regexp.MustCompile(`(?s)has\s+no\s+authentication\s+configuration`),
			},
			{
				Config: oidcShootConfig("", ""),
			},
		},
	})
}
