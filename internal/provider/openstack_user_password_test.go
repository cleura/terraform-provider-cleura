package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// validateUserConfig validates a cleura_openstack_user configuration the way a
// client does, announcing (or not) that it supports write-only attributes.
// Terraform and OpenTofu 1.11+ announce it. Crossplane providers built with
// Upjet run an older Terraform and don't, and neither do some other tools.
func validateUserConfig(t *testing.T, writeOnlyAllowed bool, attrs map[string]tftypes.Value) []*tfprotov6.Diagnostic {
	t.Helper()
	ctx := context.Background()
	server, err := testAccProtoV6ProviderFactories["cleura"]()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	objType, ok := schemas.ResourceSchemas["cleura_openstack_user"].ValueType().(tftypes.Object)
	if !ok {
		t.Fatal("cleura_openstack_user schema is not an object")
	}
	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		values[name] = tftypes.NewValue(typ, nil)
	}
	for name, v := range attrs {
		if _, ok := objType.AttributeTypes[name]; !ok {
			t.Fatalf("cleura_openstack_user has no attribute %q", name)
		}
		values[name] = v
	}
	config, err := tfprotov6.NewDynamicValue(objType, tftypes.NewValue(objType, values))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.ValidateResourceConfig(ctx, &tfprotov6.ValidateResourceConfigRequest{
		TypeName: "cleura_openstack_user",
		Config:   &config,
		ClientCapabilities: &tfprotov6.ValidateResourceConfigClientCapabilities{
			WriteOnlyAttributesAllowed: writeOnlyAllowed,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Diagnostics
}

func tfString(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func errorDiagnostics(diags []*tfprotov6.Diagnostic) []*tfprotov6.Diagnostic {
	var errs []*tfprotov6.Diagnostic
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			errs = append(errs, d)
		}
	}
	return errs
}

// TestOpenStackUserPasswordWithoutWriteOnlySupport covers clients that don't
// support write-only attributes, such as a Crossplane provider, which failed
// with "WriteOnly Attribute Not Allowed". They must be able to set a password.
func TestOpenStackUserPasswordWithoutWriteOnlySupport(t *testing.T) {
	diags := validateUserConfig(t, false, map[string]tftypes.Value{
		"name":     tfString("crossplane-user"),
		"password": tfString("Some-Passw0rd"),
	})
	for _, d := range errorDiagnostics(diags) {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
}

func TestOpenStackUserPasswordValidation(t *testing.T) {
	const pw = "Some-Passw0rd"
	for _, tc := range []struct {
		name             string
		writeOnlyAllowed bool
		attrs            map[string]tftypes.Value
		wantError        string // substring of an error's summary or detail; "" means no error
	}{
		{"password without write-only support", false, map[string]tftypes.Value{"password": tfString(pw)}, ""},
		{"password with write-only support", true, map[string]tftypes.Value{"password": tfString(pw)}, ""},
		{"password_wo with write-only support", true, map[string]tftypes.Value{
			"password_wo": tfString(pw), "password_wo_version": tfString("1"),
		}, ""},
		// Clients without write-only support have to use password.
		{"password_wo without write-only support", false, map[string]tftypes.Value{"password_wo": tfString(pw)}, "WriteOnly Attribute Not Allowed"},
		{"neither", true, map[string]tftypes.Value{}, "password_wo"},
		{"both", true, map[string]tftypes.Value{"password": tfString(pw), "password_wo": tfString(pw)}, "password_wo"},
		// The v0.3.x configuration, where password was the write-only one.
		{"password with password_wo_version", true, map[string]tftypes.Value{
			"password": tfString(pw), "password_wo_version": tfString("1"),
		}, "rename password to password_wo"},
		{"password_wo_version alone", true, map[string]tftypes.Value{"password_wo_version": tfString("1")}, "password_wo_version only applies to password_wo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]tftypes.Value{"name": tfString("some-user")}
			for k, v := range tc.attrs {
				attrs[k] = v
			}
			errs := errorDiagnostics(validateUserConfig(t, tc.writeOnlyAllowed, attrs))
			if tc.wantError == "" {
				for _, d := range errs {
					t.Errorf("unexpected error %s: %s", d.Summary, d.Detail)
				}
				return
			}
			for _, d := range errs {
				if strings.Contains(d.Summary, tc.wantError) || strings.Contains(d.Detail, tc.wantError) {
					return
				}
			}
			t.Errorf("no error mentioning %q; got %d error(s): %v", tc.wantError, len(errs), summaries(errs))
		})
	}
}

func summaries(diags []*tfprotov6.Diagnostic) []string {
	out := make([]string, 0, len(diags))
	for _, d := range diags {
		out = append(out, d.Summary+": "+d.Detail)
	}
	return out
}

// storedPasswordTest sets up the mock API for the stored-password tests and
// returns a config builder and a check of the password the API holds.
func storedPasswordTest(t *testing.T) (*mockIdentity, func(string) string, func(string) resource.TestCheckFunc) {
	t.Helper()
	mock := newMockIdentity()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", mock.username)
	t.Setenv("CLEURA_API_TOKEN", mock.token)

	config := func(passwordLines string) string {
		return `
provider "cleura" {
  cloud   = "public"
  region  = "Sto2"
  use_cli = false
}

resource "cleura_openstack_user" "test" {
  name = "tfpw-user"
` + passwordLines + `
}`
	}
	apiPasswordIs := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			u := mock.userByName("tfpw-user")
			if u == nil {
				return fmt.Errorf("user not found in the mock API")
			}
			if u.Password != want {
				return fmt.Errorf("the API has password %q, want %q", u.Password, want)
			}
			return nil
		}
	}
	return mock, config, apiPasswordIs
}

func userDeleted(mock *mockIdentity) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if mock.userByName("tfpw-user") != nil {
			return fmt.Errorf("user still exists after destroy")
		}
		return nil
	}
}

// TestOpenStackUserStoredPassword drives password (stored in state) through
// create, change and import. It uses no write-only argument, so it also runs
// on Terraform before 1.11, the versions Crossplane providers built with
// Upjet use: TF_ACC_TERRAFORM_VERSION=1.5.7 go test -run TestOpenStackUserStoredPassword
func TestOpenStackUserStoredPassword(t *testing.T) {
	mock, config, apiPasswordIs := storedPasswordTest(t)
	const addr = "cleura_openstack_user.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             userDeleted(mock),
		Steps: []resource.TestStep{
			{
				Config: config(`  password = "Stored-Passw0rd"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "password", "Stored-Passw0rd"),
					resource.TestCheckNoResourceAttr(addr, "password_wo"),
					apiPasswordIs("Stored-Passw0rd"),
				),
			},
			{
				Config:           config(`  password = "Stored-Passw0rd"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// Changing the stored password updates it in place.
				Config: config(`  password = "Changed-Passw0rd"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "password", "Changed-Passw0rd"),
					apiPasswordIs("Changed-Passw0rd"),
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"password"},
			},
		},
	})
}

// TestOpenStackUserSwitchPassword switches between password and the
// write-only password_wo. Write-only arguments need Terraform 1.11 or later.
func TestOpenStackUserSwitchPassword(t *testing.T) {
	mock, config, apiPasswordIs := storedPasswordTest(t)
	const addr = "cleura_openstack_user.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             userDeleted(mock),
		Steps: []resource.TestStep{
			{
				Config: config(`  password = "Stored-Passw0rd"`),
				Check:  apiPasswordIs("Stored-Passw0rd"),
			},
			{
				// Switching to password_wo without a version sends nothing; the
				// password only leaves state.
				Config: config(`  password_wo = "WriteOnly-Passw0rd"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "password"),
					resource.TestCheckNoResourceAttr(addr, "password_wo"),
					apiPasswordIs("Stored-Passw0rd"),
				),
			},
			{
				// A version marker sends the write-only password.
				Config: config("  password_wo         = \"WriteOnly-Passw0rd\"\n  password_wo_version = \"1\""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "password_wo"),
					resource.TestCheckResourceAttr(addr, "password_wo_version", "1"),
					apiPasswordIs("WriteOnly-Passw0rd"),
				),
			},
			{
				// Back to a stored password: sent, and stored.
				Config: config(`  password = "Back-Passw0rd"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "password", "Back-Passw0rd"),
					resource.TestCheckNoResourceAttr(addr, "password_wo_version"),
					apiPasswordIs("Back-Passw0rd"),
				),
			},
		},
	})
}
