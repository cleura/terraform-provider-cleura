package provider

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	api "github.com/cleura/cleura-client-go/api"
)

var _ resource.Resource = (*shootKubeconfigResource)(nil)

var _ resource.ResourceWithModifyPlan = (*shootKubeconfigResource)(nil)

func NewGardenerShootKubeconfigResource() resource.Resource {
	return &shootKubeconfigResource{}
}

type shootKubeconfigResource struct {
	config *ProviderConfig
}

func (r *shootKubeconfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

type shootKubeconfigResourceModel struct {
	Kubeconfig               types.String `tfsdk:"kubeconfig"`
	ShootName                types.String `tfsdk:"shoot_name"`
	LastApplied              types.String `tfsdk:"last_applied"`
	ExpirationSeconds        types.Int64  `tfsdk:"expiration_seconds"`
	RenewBeforeExpirySeconds types.Int64  `tfsdk:"renew_before_expiry_seconds"`
}

func (r *shootKubeconfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_shoot_kubeconfig"
}

func (r *shootKubeconfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"kubeconfig": schema.StringAttribute{
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"last_applied": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"shoot_name": schema.StringAttribute{
				Required: true,
				// Bug #8: changing the target shoot must reissue the kubeconfig
				// (Update does not regenerate it), so force a replace.
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"expiration_seconds": schema.Int64Attribute{
				Required: true,
				// Bug #8: changing the validity must reissue the kubeconfig.
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"renew_before_expiry_seconds": schema.Int64Attribute{
				Computed: true,
				Optional: true,
				Default:  int64default.StaticInt64(0),
			},
		},
	}
}

func (r *shootKubeconfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	// Guard project_id at plan time (Create needs it): otherwise plan is green
	// and only apply fails with "Missing project_id". Placed after the destroy
	// early-return above because Delete is a no-op that needs no project_id.
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	var plan shootKubeconfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.LastApplied.ValueString() != "" {
		generatedAt, err := time.Parse(time.RFC3339, plan.LastApplied.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("failed to parse last_applied", err.Error())
			return
		}

		validTo := (plan.ExpirationSeconds.ValueInt64() - plan.RenewBeforeExpirySeconds.ValueInt64()) + generatedAt.Unix()
		if time.Now().Unix() > validTo {
			plan.LastApplied = types.StringUnknown()
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("last_applied"))
			resp.Diagnostics.AddWarning("Kubeconfig expired", "The kubeconfig expiration specified has elapsed, resource will be recreated")
		}
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *shootKubeconfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	var data shootKubeconfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	reqBody := api.GardenerCreateShootAdminKubeConfigRequest{
		ExpirationSeconds: int(data.ExpirationSeconds.ValueInt64()),
	}
	response, err := r.config.Client.GardenerCreateShootAdminKubeConfig(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.ShootName.ValueString(), reqBody)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create kubeconfig for Gardener cluster", err.Error())
		return
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read response body", err.Error())
		return
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		resp.Diagnostics.AddError(fmt.Sprintf("API error %d", response.StatusCode), string(body))
		return
	}

	data.Kubeconfig = types.StringValue(string(body))
	data.LastApplied = types.StringValue(time.Now().Format(time.RFC3339))

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *shootKubeconfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data shootKubeconfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *shootKubeconfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data shootKubeconfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *shootKubeconfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data shootKubeconfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
}
