package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*openstackProjectResource)(nil)
	_ resource.ResourceWithImportState = (*openstackProjectResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*openstackProjectResource)(nil)
)

func NewOpenStackProjectResource() resource.Resource {
	return &openstackProjectResource{}
}

type openstackProjectResource struct {
	config *ProviderConfig
}

type openstackProjectModel struct {
	ID          types.String `tfsdk:"id"`
	DomainID    types.String `tfsdk:"domain_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
}

// openstackProjectNamePattern mirrors the API's constraint on project names.
var openstackProjectNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.\-:åäöÅÄÖ ]+$`)

func (r *openstackProjectResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *openstackProjectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_openstack_project"
}

func (r *openstackProjectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an OpenStack (Keystone) project in your Cleura account. Projects live in an " +
			"OpenStack domain; by default the provider uses the domain that serves the provider's `region`, " +
			"and `domain_id` overrides that. `name`, `description`, and `enabled` are updated in place.\n\n" +
			"**Creating a project is irreversible: Cleura projects cannot be deleted, only disabled.** " +
			"`terraform destroy` therefore sets `enabled = false`, removes the project from state, and leaves the " +
			"disabled project in Cleura, where it still counts toward the account's project quota. Re-enable it " +
			"from the Cleura Cloud control panel or by importing it again with `enabled = true`.\n\n" +
			"Reads use the project listing available to the authenticated user, so the API user configured " +
			"on the provider must have access to the project to refresh it. Import an existing project with its ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "OpenStack project ID (32 hex characters), assigned by Cleura. Also the import ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"domain_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack domain the project is created in. Defaults to the account's domain " +
					"that serves the provider's `region` (or the account's only domain). Set it explicitly when the " +
					"account has several domains for the region; list them with `cleura openstack domain list`. " +
					"Changing it forces a new project.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Project name. Letters, digits, spaces, and `_ . - :` are allowed. Must be unique " +
					"within the domain. Updated in place.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(openstackProjectNamePattern, "may only contain letters, digits, spaces, and _ . - :"),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Free-text description. Removing it from the configuration clears it on the " +
					"project. Updated in place. The API accepts only letters, numbers, spaces and the punctuation " +
					"`. , _ : / -` — other characters are rejected at apply time.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				MarkdownDescription: "Whether the project is enabled. Defaults to `true`. A disabled project keeps its " +
					"resources but cannot be used. Updated in place; destroying the resource sets this to `false`.",
			},
		},
	}
}

// ModifyPlan warns at plan time about the two irreversible facts of Cleura
// projects: creation cannot be undone, and destroy only disables.
//
// A replace (the only trigger is a changed domain_id) is both at once, and it
// reaches this function with state AND plan non-null, so it needs its own case:
// the framework hands the resource's ModifyPlan an empty RequiresReplace, so
// the attribute plan modifier's decision is not visible here and the change has
// to be detected by comparing plan with state.
func (r *openstackProjectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.State.Raw.IsNull() && !req.Plan.Raw.IsNull() {
		var plan, state openstackProjectModel
		resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !plan.DomainID.IsNull() && !plan.DomainID.IsUnknown() && !state.DomainID.IsNull() &&
			!plan.DomainID.Equal(state.DomainID) {
			resp.Diagnostics.AddAttributeWarning(path.Root("domain_id"),
				"Project will be replaced: a second project will be created and the current one only disabled",
				fmt.Sprintf("Changing domain_id (%s → %s) cannot be applied in place, so Terraform will create a "+
					"new project in the new domain and disable %q (%s) — Cleura projects cannot be deleted, and the "+
					"disabled one keeps counting toward the account's project quota. Both projects will exist "+
					"afterwards. Consider managing the project in the new domain as a separate resource instead.",
					state.DomainID.ValueString(), plan.DomainID.ValueString(), state.Name.ValueString(), state.ID.ValueString()))
		}
		return
	}
	switch {
	case req.State.Raw.IsNull() && !req.Plan.Raw.IsNull():
		var plan openstackProjectModel
		resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.AddAttributeWarning(path.Root("name"),
			"Project creation is irreversible",
			fmt.Sprintf("OpenStack project %q will be created. Cleura projects cannot be deleted, only disabled, "+
				"and a disabled project still counts toward the account's project quota. Make sure the project is "+
				"wanted before applying.", plan.Name.ValueString()))
	case !req.State.Raw.IsNull() && req.Plan.Raw.IsNull():
		var state openstackProjectModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.AddWarning(
			"Destroy will disable the project, not delete it",
			fmt.Sprintf("Cleura projects cannot be deleted. Destroying cleura_openstack_project %q (%s) sets "+
				"enabled = false and removes it from Terraform state; the disabled project remains in Cleura.",
				state.Name.ValueString(), state.ID.ValueString()))
	}
}

func (r *openstackProjectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan openstackProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, plan.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	body := api.OpenStackIdentityCreateProjectRequest{Name: plan.Name.ValueString()}
	if !plan.Description.IsNull() {
		body.Description = plan.Description.ValueStringPointer()
	}
	response, err := r.config.Client.OpenStackIdentityCreateProject(ctx, domainID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create OpenStack project", err.Error())
		return
	}
	var project api.OpenStackIdentityProject
	if err := decodeJSON(response, &project); err != nil {
		resp.Diagnostics.AddError("Failed to create OpenStack project", err.Error())
		return
	}

	// Capture the desired enabled state BEFORE the API response is mapped over
	// the model: setOpenStackProjectState overwrites plan.Enabled with what the
	// API returned (always true on create), so reading it afterwards would skip
	// the follow-up edit and leave state disagreeing with the plan.
	wantEnabled := plan.Enabled.ValueBool()

	// The project now exists. Persist state before any follow-up call so a
	// failure below leaves a tracked resource rather than an orphan.
	setOpenStackProjectState(&plan, &project)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// API WORKAROUND: the create call cannot set enabled, so a project planned
	// as disabled needs a follow-up edit.
	if !wantEnabled {
		disabled := false
		updated, err := r.edit(ctx, domainID, project.Id, api.OpenStackIdentityEditProjectRequest{Enabled: &disabled})
		if err != nil {
			// The project exists but is still enabled, so the apply has to fail.
			// Terraform then marks the resource tainted and would REPLACE it on
			// the next apply — which cannot work here: the old project can only
			// be disabled, and creating the replacement fails because the name
			// is still taken. Spell the recovery out, or the user is stuck with
			// an unexplained duplicate-name error.
			resp.Diagnostics.AddError(
				"Failed to disable the OpenStack project after creating it",
				fmt.Sprintf("Project %q was created (ID %s) but could not be disabled, so it is enabled in Cleura "+
					"while your configuration asks for enabled = false.\n\nThe project itself is fine and is "+
					"recorded in Terraform state, but Terraform has marked the resource as tainted and would try to "+
					"replace it on the next apply. That cannot succeed: Cleura projects can only be disabled, never "+
					"deleted, so the name stays taken and the replacement fails.\n\nTo recover, clear the taint and "+
					"apply again — the second apply only disables the existing project:\n\n"+
					"    terraform untaint cleura_openstack_project.<name>\n    terraform apply\n\nAPI error: %s",
					plan.Name.ValueString(), project.Id, err.Error()),
			)
			return
		}
		setOpenStackProjectState(&plan, updated)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *openstackProjectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state openstackProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projects, err := listProjects(ctx, r.config)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read OpenStack project", err.Error())
		return
	}
	project, found := findProjectByID(projects, state.ID.ValueString())
	if !found {
		// API WORKAROUND: the listing only covers projects visible to the
		// authenticated account user, so absence is not proof of deletion. Probe
		// the project directly before dropping it from state (not possible
		// without a domain, i.e. on an import of an unknown ID, where "not
		// found" is the right answer).
		if domainID := state.DomainID.ValueString(); domainID != "" {
			exists, err := projectExists(ctx, r.config, domainID, state.ID.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("Failed to read OpenStack project", err.Error())
				return
			}
			if exists {
				resp.Diagnostics.AddWarning(
					"OpenStack project not visible to the provider's credentials",
					fmt.Sprintf("Project %q (%s) still exists but is not among the projects the authenticated "+
						"user can access, so its attributes could not be refreshed and the previous state was kept. "+
						"Grant the provider's API user access to the project to manage it fully.",
						state.Name.ValueString(), state.ID.ValueString()),
				)
				return
			}
		}
		resp.State.RemoveResource(ctx)
		return
	}

	setOpenStackProjectState(&state, project)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *openstackProjectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan, state openstackProjectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	// PATCH merges: send only what changed. A removed description is cleared
	// by sending "" (an omitted field would be left untouched).
	body := api.OpenStackIdentityEditProjectRequest{}
	changed := false
	if !plan.Name.Equal(state.Name) {
		body.Name = plan.Name.ValueStringPointer()
		changed = true
	}
	if !plan.Description.Equal(state.Description) {
		description := plan.Description.ValueString()
		body.Description = &description
		changed = true
	}
	if !plan.Enabled.Equal(state.Enabled) {
		body.Enabled = plan.Enabled.ValueBoolPointer()
		changed = true
	}
	if !changed {
		plan.ID, plan.DomainID = state.ID, state.DomainID
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	project, err := r.edit(ctx, domainID, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update OpenStack project", err.Error())
		return
	}
	setOpenStackProjectState(&plan, project)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete disables the project. Not a workaround: Cleura projects cannot be
// deleted by design, and disabling is the intended substitute.
func (r *openstackProjectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state openstackProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	disabled := false
	_, err = r.edit(ctx, domainID, state.ID.ValueString(), api.OpenStackIdentityEditProjectRequest{Enabled: &disabled})
	if errors.Is(err, errNotFound) {
		// Already gone: the destroy goal is met.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to disable OpenStack project", err.Error())
		return
	}
	resp.Diagnostics.AddWarning(
		"OpenStack project disabled, not deleted",
		fmt.Sprintf("Cleura projects cannot be deleted. Project %q (%s) has been disabled and "+
			"removed from Terraform state; it still exists in Cleura, where it can be re-enabled, and can be "+
			"imported again with `terraform import`.",
			state.Name.ValueString(), state.ID.ValueString()),
	)
}

func (r *openstackProjectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// edit PATCHes the project and returns the API's view of it.
func (r *openstackProjectResource) edit(ctx context.Context, domainID, id string, body api.OpenStackIdentityEditProjectRequest) (*api.OpenStackIdentityProject, error) {
	response, err := r.config.Client.OpenStackIdentityEditProject(ctx, domainID, id, body)
	if err != nil {
		return nil, err
	}
	var project api.OpenStackIdentityProject
	if err := decodeJSON(response, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// setOpenStackProjectState copies the API's project into the model.
func setOpenStackProjectState(m *openstackProjectModel, p *api.OpenStackIdentityProject) {
	m.ID = types.StringValue(p.Id)
	m.DomainID = types.StringValue(p.DomainId)
	m.Name = types.StringValue(p.Name)
	m.Description = optionalStringValue(p.Description)
	m.Enabled = types.BoolValue(p.Enabled)
}
