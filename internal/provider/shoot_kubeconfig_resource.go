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
	"github.com/hashicorp/terraform-plugin-framework/types"

	api "github.com/cleura/terraform-provider-cleura/api"
	cleura "github.com/cleura/terraform-provider-cleura/client"
)

var _ resource.Resource = (*shootKubeconfigResource)(nil)

var _ resource.ResourceWithModifyPlan = (*shootKubeconfigResource)(nil)

func NewShootKubeconfigResource() resource.Resource {
	return &shootKubeconfigResource{}
}

type shootKubeconfigResource struct {
	client *cleura.Client
}

func (r *shootKubeconfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*cleura.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *cleura.Client, got %T", req.ProviderData),
		)
		return
	}

	r.client = client
}

type shootKubeconfigResourceModel struct {
	Kubeconfig               types.String `tfsdk:"kubeconfig"`
	ShootName                types.String `tfsdk:"shoot_name"`
	GardenerRegionTag        types.String `tfsdk:"gardener_region_tag"`
	OpenStackProjectId       types.String `tfsdk:"open_stack_project_id"`
	OpenStackRegionTag       types.String `tfsdk:"open_stack_region_tag"`
	LastApplied              types.String `tfsdk:"last_applied"`
	ExpirationSeconds        types.Int64  `tfsdk:"expiration_seconds"`
	RenewBeforeExpirySeconds types.Int64  `tfsdk:"renew_before_expiry_seconds"`
}

func (r *shootKubeconfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_shoot_kubeconfig"
}

func (r *shootKubeconfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"kubeconfig": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
			},
			"last_applied": schema.StringAttribute{
				Computed: true,
			},
			"shoot_name": schema.StringAttribute{
				Required: true,
			},
			"expiration_seconds": schema.Int64Attribute{
				Required: true,
			},
			"renew_before_expiry_seconds": schema.Int64Attribute{
				Computed: true,
				Optional: true,
				Default:  int64default.StaticInt64(0),
			},
			"gardener_region_tag": schema.StringAttribute{
				Required: true,
			},
			"open_stack_region_tag": schema.StringAttribute{
				Required: true,
			},
			"open_stack_project_id": schema.StringAttribute{
				Required: true,
			},
		},
	}
}

func (r *shootKubeconfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy: plan is null, nothing to modify
	if req.Plan.Raw.IsNull() {
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

		valid_to := (plan.ExpirationSeconds.ValueInt64() - plan.RenewBeforeExpirySeconds.ValueInt64()) + generatedAt.Unix()
		now := time.Now().Unix()
		if now > valid_to {
			plan.LastApplied = types.StringUnknown()
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("last_applied"))
			resp.Diagnostics.AddWarning("Kubeconfig expired", "The kubeconfig expiration specified has elapsed, resource will be recreated")
		}
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *shootKubeconfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data shootKubeconfigResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Create API call logic
	reqBody := api.GardenerCreateShootAdminKubeConfigRequest{
		ExpirationSeconds: int(data.ExpirationSeconds.ValueInt64()),
	}
	response, err := r.client.GardenerCreateShootAdminKubeConfig(ctx, data.GardenerRegionTag.ValueString(), data.OpenStackRegionTag.ValueString(), data.OpenStackProjectId.ValueString(), data.ShootName.ValueString(), reqBody)
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

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *shootKubeconfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data shootKubeconfigResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Read API call logic

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *shootKubeconfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data shootKubeconfigResourceModel

	// Read Terraform plan data into the model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Update API call logic

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *shootKubeconfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data shootKubeconfigResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Kubeconfig cannot be removed as of now
}
