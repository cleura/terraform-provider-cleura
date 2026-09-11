package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*gardenerBootstrapResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*gardenerBootstrapResource)(nil)
	_ resource.ResourceWithImportState    = (*gardenerBootstrapResource)(nil)
	_ resource.ResourceWithValidateConfig = (*gardenerBootstrapResource)(nil)
)

func NewGardenerBootstrapResource() resource.Resource {
	return &gardenerBootstrapResource{}
}

type gardenerBootstrapResource struct {
	config *ProviderConfig
}

type gardenerBootstrapModel struct {
	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
	Cloud     types.String `tfsdk:"cloud"`
	Region    types.String `tfsdk:"region"`
}

func (r *gardenerBootstrapResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *gardenerBootstrapResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_bootstrap"
}

func (r *gardenerBootstrapResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Prepares an OpenStack project for Gardener, which a project must be before any " +
			"`cleura_gardener_shoot` can be created in it. Projects created through the Cleura console are usually " +
			"bootstrapped already; a project created by `cleura_openstack_project` is not.\n\n" +
			"~> **This resource is one-way.** The API offers only `POST .../bootstrap`: there is no endpoint to read " +
			"whether a project is bootstrapped and none to undo it. So this resource cannot detect drift, and " +
			"destroying it only removes it from Terraform state — the project stays bootstrapped.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the bootstrap, in the form `<cloud>/<region>/<project_id>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack project to bootstrap. Defaults to the provider's `project_id`. Set it " +
					"explicitly to bootstrap a project created in the same configuration — the provider's `project_id` " +
					"must be known before the run starts, so it cannot refer to a `cleura_openstack_project` managed " +
					"by this provider. Changing it forces replacement (a bootstrap of the new project).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Cleura cloud the project was bootstrapped in, taken from the provider configuration.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"region": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "OpenStack region the project was bootstrapped in, taken from the provider configuration.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig runs before the provider is configured, so it can only check
// the configuration itself. The provider-level fallback is checked in Create.
func (r *gardenerBootstrapResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config gardenerBootstrapModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !config.ProjectID.IsNull() && !config.ProjectID.IsUnknown() && config.ProjectID.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"),
			"Empty project_id",
			"project_id was set to an empty string. Remove it to fall back to the provider's project_id, or give a project ID.")
	}
}

func (r *gardenerBootstrapResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	switch {
	case req.State.Raw.IsNull() && !req.Plan.Raw.IsNull():
		var plan gardenerBootstrapModel
		resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
		target := "the provider's project_id"
		if !plan.ProjectID.IsNull() && !plan.ProjectID.IsUnknown() {
			target = fmt.Sprintf("project %s", plan.ProjectID.ValueString())
		}
		resp.Diagnostics.AddWarning(
			"Gardener bootstrap is irreversible and cannot be undone",
			fmt.Sprintf("Bootstrapping %s prepares it for Gardener and provisions supporting resources in it. The "+
				"API has no endpoint to undo a bootstrap, so destroying this resource will only forget it. Make sure "+
				"the project is the right one before applying.", target))
	case !req.State.Raw.IsNull() && req.Plan.Raw.IsNull():
		var state gardenerBootstrapModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.AddWarning(
			"Destroy only forgets the bootstrap, it does not undo it",
			fmt.Sprintf("Project %s stays bootstrapped for Gardener; the API has no teardown endpoint. Terraform "+
				"will remove the resource from state only.", state.ProjectID.ValueString()))
	}
}

func (r *gardenerBootstrapResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan gardenerBootstrapModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := plan.ProjectID.ValueString()
	if plan.ProjectID.IsNull() || plan.ProjectID.IsUnknown() {
		projectID = r.config.ProjectID
	}
	if projectID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"),
			"Missing project_id",
			"Set project_id on this resource, or set project_id on the provider configuration or the "+
				"CLEURA_PROJECT_ID environment variable.")
		return
	}

	response, err := r.config.Client.GardenerCommunicationBootstrap(ctx, r.config.Cloud, r.config.Region, projectID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to bootstrap the project for Gardener", err.Error())
		return
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read response body", err.Error())
		return
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Failed to bootstrap project %s for Gardener (HTTP %d)", projectID, response.StatusCode),
			bootstrapErrorDetail(response.StatusCode, projectID, body))
		return
	}

	plan.ProjectID = types.StringValue(projectID)
	plan.Cloud = types.StringValue(r.config.Cloud)
	plan.Region = types.StringValue(r.config.Region)
	plan.ID = types.StringValue(bootstrapID(r.config.Cloud, r.config.Region, projectID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read is deliberately a no-op: the API has no endpoint reporting whether a
// project is bootstrapped, so there is nothing to refresh against and the
// prior state is left untouched. A bootstrap undone outside Terraform (which
// the API also offers no way to do) would go unnoticed.
func (r *gardenerBootstrapResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
}

// Update is unreachable: every configurable attribute forces replacement. It
// exists to satisfy the resource interface.
func (r *gardenerBootstrapResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan gardenerBootstrapModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the resource from state without calling the API: there is no
// endpoint to undo a bootstrap. ModifyPlan warns about this at plan time.
func (r *gardenerBootstrapResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// ImportState adopts an already-bootstrapped project by its ID. Nothing is
// verified, because the API offers no way to ask whether a project is
// bootstrapped; importing a project that never was will make Terraform believe
// it is.
func (r *gardenerBootstrapResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	projectID := strings.TrimSpace(req.ID)
	if projectID == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Import a Gardener bootstrap by its OpenStack project ID.")
		return
	}
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cloud"), r.config.Cloud)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), r.config.Region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"),
		bootstrapID(r.config.Cloud, r.config.Region, projectID))...)
}

func bootstrapID(cloud, region, projectID string) string {
	return fmt.Sprintf("%s/%s/%s", cloud, region, projectID)
}

// bootstrapErrorDetail turns the API's bootstrap failures into something a
// user can act on.
//
// API WORKAROUND: an unknown project id answers 500 "Internal Server Error"
// rather than a 404, so the status alone cannot be trusted to mean an outage;
// the likeliest cause is named here instead.
func bootstrapErrorDetail(status int, projectID string, body []byte) string {
	detail := apiErrorDetail(status, body)
	if status == http.StatusInternalServerError {
		return detail + fmt.Sprintf("\n\nThe API answers an unknown project ID with this error as well. Check that "+
			"project %q exists in the provider's region and that your account can reach it.", projectID)
	}
	return detail
}
