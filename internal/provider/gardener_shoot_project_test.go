package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// shootSchema is the resource's real schema, project_id included.
func shootSchema(t *testing.T) fwresource.SchemaResponse {
	t.Helper()
	var resp fwresource.SchemaResponse
	(&GardenerShootResource{}).Schema(context.Background(), fwresource.SchemaRequest{}, &resp)
	return resp
}

// TestShootSchemaCarriesProjectID guards the whole approach: project_id is added
// to a generated schema in a hand-written wrapper, and the model that reads it
// embeds the generated struct by value. If the framework ever stops mapping an
// embedded struct, every Plan.Get in the resource breaks — this catches it.
func TestShootSchemaCarriesProjectID(t *testing.T) {
	ctx := context.Background()
	resp := shootSchema(t)
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema is invalid: %v", diags)
	}

	attr, ok := resp.Schema.Attributes["project_id"]
	if !ok {
		t.Fatal("project_id is missing from the shoot schema")
	}
	if !attr.IsOptional() || !attr.IsComputed() {
		t.Errorf("project_id must be Optional+Computed, got optional=%v computed=%v",
			attr.IsOptional(), attr.IsComputed())
	}

	// The schema's object type must match shootModel, embedded field included.
	objType := resp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if _, ok := objType.AttributeTypes["project_id"]; !ok {
		t.Fatal("project_id is absent from the schema's object type")
	}

	// Round-trip a state value through the embedded struct. This is the check
	// that the wrapper actually works: a mismatch between struct and object
	// type fails here rather than at apply time.
	vals := map[string]tftypes.Value{}
	for name, ty := range objType.AttributeTypes {
		vals[name] = tftypes.NewValue(ty, nil)
	}
	vals["name"] = tftypes.NewValue(tftypes.String, "some-shoot")
	vals["project_id"] = tftypes.NewValue(tftypes.String, "proj-1234")

	state := tfsdk.State{Raw: tftypes.NewValue(objType, vals), Schema: resp.Schema}
	var data shootModel
	if diags := state.Get(ctx, &data); diags.HasError() {
		t.Fatalf("reading state into shootModel failed: %v", diags)
	}
	if got := data.ProjectID; got != types.StringValue("proj-1234") {
		t.Errorf("ProjectID = %v, want proj-1234", got)
	}
	if got := data.Name; got != types.StringValue("some-shoot") {
		t.Errorf("embedded Name = %v, want some-shoot (embedded fields did not map)", got)
	}
}

// TestProjectIDMigrationDoesNotForceReplace is the guard on the upgrade path.
//
// Every shoot in state today predates project_id, so its stored value is null.
// The attribute is Optional+Computed and forces replacement when it changes, so
// if a null prior state planned as unknown, the first plan after upgrading
// would destroy and recreate a live cluster. UseStateForUnknown has to collapse
// the unknown to null first, leaving RequiresReplace with nothing to do.
func TestProjectIDMigrationDoesNotForceReplace(t *testing.T) {
	ctx := context.Background()
	sch := shootSchema(t).Schema
	objType := sch.Type().TerraformType(ctx).(tftypes.Object)

	// A non-null object for State.Raw/Plan.Raw: the modifiers read those to tell
	// create and destroy apart from update.
	vals := map[string]tftypes.Value{}
	for name, ty := range objType.AttributeTypes {
		vals[name] = tftypes.NewValue(ty, nil)
	}
	vals["name"] = tftypes.NewValue(tftypes.String, "existing-shoot")
	raw := tftypes.NewValue(objType, vals)

	attr, ok := sch.Attributes["project_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("project_id is not a StringAttribute, got %T", sch.Attributes["project_id"])
	}
	if len(attr.PlanModifiers) == 0 {
		t.Fatal("project_id has no plan modifiers")
	}

	req := planmodifier.StringRequest{
		Path:        path.Root("project_id"),
		Config:      tfsdk.Config{Raw: raw, Schema: sch},
		ConfigValue: types.StringNull(), // not set in the configuration
		State:       tfsdk.State{Raw: raw, Schema: sch},
		StateValue:  types.StringNull(), // written before project_id existed
		Plan:        tfsdk.Plan{Raw: raw, Schema: sch},
		PlanValue:   types.StringUnknown(), // Optional+Computed, so Terraform plans unknown
	}

	for i, mod := range attr.PlanModifiers {
		modResp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		mod.PlanModifyString(ctx, req, modResp)
		if modResp.Diagnostics.HasError() {
			t.Fatalf("plan modifier %d errored: %v", i, modResp.Diagnostics)
		}
		if modResp.RequiresReplace {
			t.Fatalf("plan modifier %d demands replacement for a shoot that only lacks project_id; "+
				"upgrading the provider would destroy every existing cluster", i)
		}
		req.PlanValue = modResp.PlanValue
	}

	if !req.PlanValue.IsNull() {
		t.Errorf("planned project_id = %v, want null so it matches prior state", req.PlanValue)
	}
}

func TestResolveProjectID(t *testing.T) {
	for _, tc := range []struct {
		name, explicit, provider, want string
		wantErr                        bool
	}{
		{name: "the resource's own value wins", explicit: "res-proj", provider: "prov-proj", want: "res-proj"},
		{name: "falls back to the provider", explicit: "", provider: "prov-proj", want: "prov-proj"},
		{name: "neither set is an error", explicit: "", provider: "", wantErr: true},
		{name: "resource value alone is enough", explicit: "res-proj", provider: "", want: "res-proj"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveProjectID(&ProviderConfig{ProjectID: tc.provider}, tc.explicit)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error when no project is named anywhere")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveProjectID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseShootImportID(t *testing.T) {
	for _, tc := range []struct {
		name, id, wantProject, wantShoot string
		wantErr                          bool
	}{
		// The pre-existing format, which must keep working.
		{name: "bare name leaves the project to the provider", id: "my-shoot", wantShoot: "my-shoot"},
		{name: "project and name", id: "proj-1234/my-shoot", wantProject: "proj-1234", wantShoot: "my-shoot"},
		{name: "surrounding space is trimmed", id: "  proj-1234 / my-shoot ", wantProject: "proj-1234", wantShoot: "my-shoot"},
		{name: "empty", id: "", wantErr: true},
		{name: "empty shoot name", id: "proj-1234/", wantErr: true},
		{name: "empty project", id: "/my-shoot", wantErr: true},
		{name: "too many segments", id: "a/b/c", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, shoot, err := parseShootImportID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got project=%q shoot=%q", tc.id, project, shoot)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if project != tc.wantProject || shoot != tc.wantShoot {
				t.Errorf("parseShootImportID(%q) = (%q, %q), want (%q, %q)",
					tc.id, project, shoot, tc.wantProject, tc.wantShoot)
			}
		})
	}
}
