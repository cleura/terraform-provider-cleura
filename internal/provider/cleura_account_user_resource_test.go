package provider

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	api "github.com/cleura/terraform-provider-cleura/api"
)

// These are white-box unit tests for the account-user mapping layer. They take no
// network and no Terraform runtime: they exercise the pure translation functions
// where "inconsistent result after apply" bugs originate (the request/response
// asymmetries documented in cleura-api-wishlist.md item 13).

func TestOptionalString(t *testing.T) {
	if got := optionalString(types.StringNull()); got != nil {
		t.Errorf("null -> %q, want nil", *got)
	}
	if got := optionalString(types.StringUnknown()); got != nil {
		t.Errorf("unknown -> %q, want nil", *got)
	}
	got := optionalString(types.StringValue("jane"))
	if got == nil || *got != "jane" {
		t.Errorf("value -> %v, want pointer to \"jane\"", got)
	}
}

func TestIPRestrictionsToAPI(t *testing.T) {
	if got := ipRestrictionsToAPI(nil); got != nil {
		t.Errorf("nil -> %v, want nil", *got)
	}
	if got := ipRestrictionsToAPI([]types.String{}); got != nil {
		t.Errorf("empty -> %v, want nil", *got)
	}
	got := ipRestrictionsToAPI([]types.String{
		types.StringValue("10.0.0.0/8"),
		types.StringValue("192.168.0.0/16"),
	})
	want := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPrivilegesToAPI_Nil(t *testing.T) {
	if got := privilegesToAPI(nil); got != nil {
		t.Errorf("nil model -> %#v, want nil", got)
	}
	if got := privilegeToAPI(nil); got != nil {
		t.Errorf("nil category -> %#v, want nil", got)
	}
}

func TestApiToPrivileges_AllNil(t *testing.T) {
	// An API response with no privilege categories set must collapse to nil so
	// that a user who configured no privileges block sees no drift.
	if got := apiToPrivileges(api.CommonUserLoginPrivileges{}); got != nil {
		t.Errorf("empty privileges -> %#v, want nil", got)
	}
	if got := apiToPrivilege(nil); got != nil {
		t.Errorf("nil category -> %#v, want nil", got)
	}
}

// TestPrivilegesRoundTrip is the load-bearing test: a configured privilege model
// must survive model -> API -> model unchanged. This is the property the live
// "terraform plan -> No changes" run demonstrated; here it is locked in offline.
func TestPrivilegesRoundTrip(t *testing.T) {
	original := &accountUserPrivilegesModel{
		Invoice: &accountUserPrivilegeModel{
			Type: types.StringValue("read"),
			Meta: types.StringValue(""),
		},
		Users: &accountUserPrivilegeModel{
			Type: types.StringValue("full"),
			Meta: types.StringValue(""),
		},
		Openstack: &accountUserPrivilegeModel{
			Type: types.StringValue("project"),
			Meta: types.StringValue(""),
			ProjectPrivileges: []accountUserProjectPrivilegeModel{
				{
					DomainID:  types.StringValue("0123456789abcdef0123456789abcdef"),
					ProjectID: types.StringValue("fedcba9876543210fedcba9876543210"),
					Type:      types.StringValue("full"),
				},
			},
		},
		// Account, AiGateway, Application, Monitoring intentionally left nil.
	}

	got := apiToPrivileges(*privilegesToAPI(original))
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("round-trip mismatch:\n got  %#v\n want %#v", got, original)
	}
}

func TestMapUserToModel(t *testing.T) {
	email := "jane.doe@example.org"
	first := "Jane"
	last := "Doe"
	lang := "en"
	pending := "new@example.org"

	u := &api.CommonUserLogin{
		Id:           42989,
		Name:         "jdoe", // response uses `name`; model must expose it as `username`
		Email:        &email,
		Firstname:    &first,
		Lastname:     &last,
		Admin:        true,
		PendingEmail: &pending,
		Language:     &lang,
		IpRestrictions: []api.CommonUserLoginIpLimit{
			{Cidr: "10.0.0.0/8"}, // Created (server-set) is dropped on the way in
			{Cidr: "192.168.0.0/16"},
		},
		Privileges: api.CommonUserLoginPrivileges{
			Invoice: &api.CommonUserLoginPrivilege{Type: "read", Meta: ""},
		},
	}

	// Seed the write-only password (null) and its companion version; mapUserToModel
	// must leave both untouched.
	model := &accountUserResourceModel{
		Password:          types.StringNull(),
		PasswordWOVersion: types.StringValue("3"),
	}
	mapUserToModel(u, model)

	if model.ID.ValueString() != "42989" {
		t.Errorf("ID = %q, want \"42989\"", model.ID.ValueString())
	}
	if model.Username.ValueString() != "jdoe" {
		t.Errorf("Username (from response.name) = %q, want \"jdoe\"", model.Username.ValueString())
	}
	if model.Email.ValueString() != email {
		t.Errorf("Email = %q, want %q", model.Email.ValueString(), email)
	}
	if !model.Admin.ValueBool() {
		t.Error("Admin = false, want true")
	}
	if model.Language.ValueString() != lang {
		t.Errorf("Language = %q, want %q", model.Language.ValueString(), lang)
	}

	wantIPs := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if len(model.IPRestrictions) != len(wantIPs) {
		t.Fatalf("IPRestrictions len = %d, want %d", len(model.IPRestrictions), len(wantIPs))
	}
	for i, ip := range model.IPRestrictions {
		if ip.ValueString() != wantIPs[i] {
			t.Errorf("IPRestrictions[%d] = %q, want %q", i, ip.ValueString(), wantIPs[i])
		}
	}

	if !model.Password.IsNull() {
		t.Errorf("Password = %q, want null (write-only, never written to state)", model.Password.ValueString())
	}
	if model.PasswordWOVersion.ValueString() != "3" {
		t.Errorf("PasswordWOVersion = %q, want \"3\" (must be preserved)", model.PasswordWOVersion.ValueString())
	}

	if model.Privileges == nil || model.Privileges.Invoice == nil {
		t.Fatal("Privileges.Invoice = nil, want set")
	}
	if got := model.Privileges.Invoice.Type.ValueString(); got != "read" {
		t.Errorf("Privileges.Invoice.Type = %q, want \"read\"", got)
	}
}

func TestMapUserToModel_EmptyOptionals(t *testing.T) {
	u := &api.CommonUserLogin{Id: 1, Name: "min"} // all optionals nil/empty
	model := &accountUserResourceModel{}
	mapUserToModel(u, model)

	if model.ID.ValueString() != "1" {
		t.Errorf("ID = %q, want \"1\"", model.ID.ValueString())
	}
	if !model.Email.IsNull() {
		t.Error("Email should be null when the API returns no email")
	}
	if model.IPRestrictions != nil {
		t.Errorf("IPRestrictions = %v, want nil when none returned", model.IPRestrictions)
	}
	if model.Privileges != nil {
		t.Errorf("Privileges = %#v, want nil when none returned", model.Privileges)
	}
}

func TestDecodeUserResponse(t *testing.T) {
	// The create/edit response is a bare Common_UserLogin object (no envelope).
	body := `{"id":7,"name":"jdoe","email":"j@x.org","privileges":{"invoice":{"type":"read","meta":""}}}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}

	var diags diag.Diagnostics
	user, ok := decodeUserResponse(resp, &diags, "test")
	if !ok {
		t.Fatalf("ok = false, diags: %v", diags)
	}
	if user.Id != 7 || user.Name != "jdoe" {
		t.Errorf("decoded id/name = %d/%q, want 7/\"jdoe\"", user.Id, user.Name)
	}
}

func TestDecodeUserResponse_Error(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"bad"}`))}

	var diags diag.Diagnostics
	if _, ok := decodeUserResponse(resp, &diags, "test"); ok {
		t.Fatal("ok = true on HTTP 400, want false")
	}
	if !diags.HasError() {
		t.Fatal("expected a diagnostic on HTTP 400")
	}
}
