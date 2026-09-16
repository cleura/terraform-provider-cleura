package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

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
	ProjectID                types.String `tfsdk:"project_id"`
	ExpiresAt                types.String `tfsdk:"expires_at"`
	LastApplied              types.String `tfsdk:"last_applied"`
	ExpirationSeconds        types.Int64  `tfsdk:"expiration_seconds"`
	RenewBeforeExpirySeconds types.Int64  `tfsdk:"renew_before_expiry_seconds"`
}

func (r *shootKubeconfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_shoot_kubeconfig"
}

func (r *shootKubeconfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Issues and manages a short-lived administrator kubeconfig for a Gardener shoot cluster. The kubeconfig is minted once, at creation, with a fixed validity (expiration_seconds); Terraform then rotates it by replacing the resource as the credential nears expiry (renew_before_expiry_seconds). Because the API only generates a kubeconfig at issuance and never regenerates it on update, changing shoot_name or expiration_seconds forces replacement. cloud, region, project_id, and credentials come from the provider configuration.",
		Attributes: map[string]schema.Attribute{
			"kubeconfig": schema.StringAttribute{
				Description:   "The generated administrator kubeconfig for the shoot cluster, rendered as YAML. This is the full admin credential and is marked sensitive.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"last_applied": schema.StringAttribute{
				Description: "Deprecated: use expires_at. RFC3339 timestamp recording when the kubeconfig was issued, from which the provider used to estimate expiry before the API returned it. Still written, and still used for resources whose state predates expires_at.",
				DeprecationMessage: "last_applied is superseded by expires_at, which the API now returns directly. " +
					"Rotation is driven by expires_at; last_applied is only consulted for state written before it existed.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"expires_at": schema.StringAttribute{
				Description:   "RFC3339 timestamp at which the kubeconfig expires, as reported by the API when it was minted. Drives rotation together with renew_before_expiry_seconds.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"shoot_name": schema.StringAttribute{
				Description: "Name of the Gardener shoot cluster to issue the admin kubeconfig for. Changing it forces replacement, because the kubeconfig is only minted at creation and is not regenerated on update.",
				Required:    true,
				// Bug #8: changing the target shoot must reissue the kubeconfig
				// (Update does not regenerate it), so force a replace.
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"expiration_seconds": schema.Int64Attribute{
				Description: "Requested validity of the issued kubeconfig, in seconds from when it is minted. Changing it forces replacement so a new kubeconfig can be issued with the new lifetime.",
				Required:    true,
				// Bug #8: changing the validity must reissue the kubeconfig.
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "OpenStack project holding the shoot. Defaults to the provider's project_id. Set it to " +
					"match the project of the cleura_gardener_shoot this kubeconfig is for, when that is not the " +
					"provider's — a mismatch cannot be detected in advance and surfaces as a 404 at apply time. " +
					"Recorded in state; changing it forces a new kubeconfig.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"renew_before_expiry_seconds": schema.Int64Attribute{
				Description: "Seconds before the kubeconfig's expiry at which Terraform proactively rotates it (by replacing the resource on the next plan/apply). Must be less than expiration_seconds. Defaults to 0, meaning the kubeconfig is rotated only after it has fully expired.",
				Computed:    true,
				Optional:    true,
				Default:     int64default.StaticInt64(0),
			},
		},
	}
}

// kubeconfigExpiry reports when the planned kubeconfig expires, and which
// attribute the answer came from so a replace can be anchored to it.
//
// expires_at is what the API reports and is authoritative. Resources created
// before the provider read it hold only last_applied, so their expiry is still
// estimated from the issue time plus the requested lifetime — that keeps an
// upgrade from rotating every existing kubeconfig on the next plan.
func kubeconfigExpiry(plan *shootKubeconfigResourceModel) (time.Time, string, bool) {
	if v := plan.ExpiresAt.ValueString(); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, "expires_at", true
		}
		return time.Time{}, "expires_at", false
	}
	if v := plan.LastApplied.ValueString(); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.Add(time.Duration(plan.ExpirationSeconds.ValueInt64()) * time.Second), "last_applied", true
		}
		return time.Time{}, "last_applied", false
	}
	// Nothing recorded yet (a create); there is no expiry to act on.
	return time.Time{}, "", false
}

// expiryDiagnostics reports an unparsable timestamp, and stays silent when
// there simply is no timestamp yet.
func expiryDiagnostics(plan *shootKubeconfigResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if v := plan.ExpiresAt.ValueString(); v != "" {
		diags.AddAttributeError(path.Root("expires_at"), "Could not read the kubeconfig expiry",
			fmt.Sprintf("expires_at holds %q, which is not an RFC3339 timestamp.", v))
		return diags
	}
	if v := plan.LastApplied.ValueString(); v != "" {
		diags.AddAttributeError(path.Root("last_applied"), "Could not read when the kubeconfig was issued",
			fmt.Sprintf("last_applied holds %q, which is not an RFC3339 timestamp.", v))
	}
	return diags
}

func (r *shootKubeconfigResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	// Guard project_id at plan time (Create needs it): otherwise plan is green
	// and only apply fails with "Missing project_id". Placed after the destroy
	// early-return above because Delete is a no-op that needs no project_id.
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan shootKubeconfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	expiry, attr, ok := kubeconfigExpiry(&plan)
	if !ok {
		resp.Diagnostics.Append(expiryDiagnostics(&plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	} else {
		renewAt := expiry.Add(-time.Duration(plan.RenewBeforeExpirySeconds.ValueInt64()) * time.Second)
		if now := time.Now(); now.After(renewAt) {
			plan.Kubeconfig = types.StringUnknown()
			plan.ExpiresAt = types.StringUnknown()
			plan.LastApplied = types.StringUnknown()
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root(attr))
			resp.Diagnostics.AddWarning(rotationWarning(now, expiry))
		}
	}

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// rotationWarning explains why a kubeconfig is being replaced.
// renew_before_expiry_seconds rotates the credential *before* it expires, so
// the summary must not report an expiry that has not happened yet — with the
// default of 0 it has, and the two cases read differently to the user.
func rotationWarning(now, expiry time.Time) (summary, detail string) {
	if now.Before(expiry) {
		return "Kubeconfig is due for renewal", fmt.Sprintf(
			"The kubeconfig expires at %s and its renewal window has been reached; the resource will be recreated "+
				"to issue a new one. The current one stays valid until then.", expiry.Format(time.RFC3339))
	}
	return "Kubeconfig expired", fmt.Sprintf(
		"The kubeconfig expired at %s; the resource will be recreated to issue a new one.", expiry.Format(time.RFC3339))
}

func (r *shootKubeconfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
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
	// The v3 endpoint returns the kubeconfig together with the expiry the API
	// actually granted, so the provider no longer has to estimate it.
	projectID, err := resolveProjectID(r.config, data.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing Cleura project_id",
			capitalizeFirst(err.Error())+". It is required for Gardener resources.")
		return
	}
	data.ProjectID = types.StringValue(projectID)

	response, err := r.config.Client.GardenerCreateShootAdminKubeConfigV3(ctx, r.config.Cloud, r.config.Region, projectID, data.ShootName.ValueString(), reqBody)
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
		resp.Diagnostics.AddError(fmt.Sprintf("API error %d", response.StatusCode),
			kubeconfigErrorDetail(response.StatusCode, projectID, data.ShootName.ValueString(), body))
		return
	}

	var issued api.GardenerAdminKubeConfig
	if err := json.Unmarshal(body, &issued); err != nil {
		resp.Diagnostics.AddError("Failed to decode the kubeconfig response", err.Error())
		return
	}
	if issued.Kubeconfig == "" {
		resp.Diagnostics.AddError("Empty kubeconfig",
			"The API returned no kubeconfig in its response. Nothing was stored; re-run the apply.")
		return
	}

	data.Kubeconfig = types.StringValue(issued.Kubeconfig)
	data.ExpiresAt = types.StringValue(issued.ExpiresAt.UTC().Format(time.RFC3339))
	data.LastApplied = types.StringValue(time.Now().UTC().Format(time.RFC3339))

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

// kubeconfigErrorDetail explains the failure a project mismatch produces.
//
// Nothing ties this resource's project_id to the project of the shoot it names,
// so pointing it at the wrong one is indistinguishable from a missing cluster:
// both answer 404. The likely cause is named rather than left to the raw body.
func kubeconfigErrorDetail(status int, projectID, shootName string, body []byte) string {
	detail := string(body)
	if status == http.StatusNotFound {
		return detail + fmt.Sprintf("\n\nNo shoot named %q exists in project %s. Check the shoot name, and that "+
			"project_id matches the project the cluster was created in — a kubeconfig cannot be issued across "+
			"projects.", shootName, projectID)
	}
	return detail
}
