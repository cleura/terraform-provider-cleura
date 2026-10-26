package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = (*volumeRecoveryServiceResource)(nil)
	_ resource.ResourceWithImportState = (*volumeRecoveryServiceResource)(nil)
)

// How Delete waits for the Recovery service to stop. Variables, so tests can
// shorten them.
var (
	recoveryServicePollInterval = 5 * time.Second
	recoveryServiceStopTimeout  = 30 * time.Minute
)

// API WORKAROUND: the Recovery service keeps recovery points for 10 or 30
// days, and the API reports any other value only at apply time. The provider
// checks at plan time instead; update the list if the accepted values change.
// See item 4 in .agent/volume-recovery-service-api-feedback.md.
var recoveryServiceRetentionDays = []int64{10, 30}

var volumeIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func NewOpenStackVolumeRecoveryServiceResource() resource.Resource {
	return &volumeRecoveryServiceResource{}
}

type volumeRecoveryServiceResource struct {
	config *ProviderConfig
}

type volumeRecoveryServiceModel struct {
	ID            types.String `tfsdk:"id"`
	VolumeID      types.String `tfsdk:"volume_id"`
	Region        types.String `tfsdk:"region"`
	ProjectID     types.String `tfsdk:"project_id"`
	RetentionDays types.Int64  `tfsdk:"retention_days"`
	Immutable     types.Bool   `tfsdk:"immutable"`
	StartedAt     types.String `tfsdk:"started_at"`
}

// volumeTarget is the volume a resource acts on, resolved once.
type volumeTarget struct {
	region, projectID string
	volumeID          uuid.UUID
}

func (r *volumeRecoveryServiceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *volumeRecoveryServiceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_openstack_volume_recovery_service"
}

func (r *volumeRecoveryServiceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Switches on Cleura's Recovery service (disaster recovery) for a block volume, for " +
			"example one created with the OpenStack provider's `openstack_blockstorage_volume_v3`. Settings change " +
			"in place. Destroying the resource switches the service off and waits until it has stopped, so the " +
			"volume can be deleted in the same run.\n\n" +
			"Import with `<volume_id>`, `<project_id>/<volume_id>` or `<region>/<project_id>/<volume_id>`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The volume's ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"volume_id": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "ID of the volume, for example `openstack_blockstorage_volume_v3.data.id`. " +
					"Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(volumeIDPattern, "must be a volume ID (a UUID)"),
				},
			},
			"region": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Region of the volume. Defaults to the provider's `region`. Changing it " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"project_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack project of the volume. Defaults to the provider's `project_id`. " +
					"Changing it forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"retention_days": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "How many days recovery points are kept: `10` or `30`. Changed in place.",
				Validators: []validator.Int64{
					int64validator.OneOf(recoveryServiceRetentionDays...),
				},
			},
			"immutable": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Whether recovery points are immutable. Defaults to `false`. Changed in place. " +
					"Immutable recovery points may prevent later changes, and destroying this resource, until " +
					"their retention has passed.",
			},
			"started_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "When the Recovery service was switched on (RFC 3339).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *volumeRecoveryServiceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var plan volumeRecoveryServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, ok := r.target(&plan, &resp.Diagnostics)
	if !ok {
		return
	}

	// A Recovery service that is already on is adopted: the request changes it
	// to the planned settings and keeps its recovery points.
	v, err := setRecoveryService(ctx, r.config, t, recoverySettings(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Failed to switch on the Recovery service", volumeErrorDetail(err, t))
		return
	}
	if !recoveryServiceOn(v) {
		resp.Diagnostics.AddError("Recovery service not active", recoveryServiceStatusDetail(v, t))
		return
	}
	setRecoveryServiceState(&plan, t, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeRecoveryServiceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var state volumeRecoveryServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, ok := r.target(&state, &resp.Diagnostics)
	if !ok {
		return
	}

	v, found, err := getVolume(ctx, r.config, t)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the volume", err.Error())
		return
	}
	if !found {
		tflog.Info(ctx, "volume is gone; removing its Recovery service from state", map[string]any{"volume_id": t.volumeID.String()})
		resp.State.RemoveResource(ctx)
		return
	}
	if !recoveryServiceOn(v) {
		tflog.Info(ctx, "Recovery service is off; removing it from state", map[string]any{"volume_id": t.volumeID.String()})
		resp.State.RemoveResource(ctx)
		return
	}
	setRecoveryServiceState(&state, t, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeRecoveryServiceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var plan, state volumeRecoveryServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The volume, region and project force a replacement, so state holds them.
	t, ok := r.target(&state, &resp.Diagnostics)
	if !ok {
		return
	}

	v, err := setRecoveryService(ctx, r.config, t, recoverySettings(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Failed to change the Recovery service", volumeErrorDetail(err, t))
		return
	}
	if !recoveryServiceOn(v) {
		resp.Diagnostics.AddError("Recovery service not active", recoveryServiceStatusDetail(v, t))
		return
	}
	setRecoveryServiceState(&plan, t, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeRecoveryServiceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var state volumeRecoveryServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, ok := r.target(&state, &resp.Diagnostics)
	if !ok {
		return
	}

	v, found, err := getVolume(ctx, r.config, t)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the volume", err.Error())
		return
	}
	if !found {
		return
	}
	// Only an active service is switched off. One that is already stopping, or
	// was switched off outside Terraform, only needs the wait below.
	if recoveryServiceOn(v) {
		if _, err := setRecoveryService(ctx, r.config, t, nil); err != nil {
			detail := volumeErrorDetail(err, t)
			if state.Immutable.ValueBool() {
				detail += "\n\nThe recovery points are immutable, which may prevent switching the service off " +
					"until their retention has passed."
			}
			resp.Diagnostics.AddError("Failed to switch off the Recovery service", detail)
			return
		}
	}
	if err := waitRecoveryServiceStopped(ctx, r.config, t); err != nil {
		resp.Diagnostics.AddError("Recovery service still stopping",
			capitalizeFirst(err.Error())+". The volume can't be deleted until the service has stopped. "+
				"Run the command again to keep waiting.")
	}
}

// ImportState accepts "<volume_id>", "<project_id>/<volume_id>" or
// "<region>/<project_id>/<volume_id>". Region and project default to the
// provider's.
func (r *volumeRecoveryServiceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	region, projectID, volumeID, err := parseVolumeRecoveryServiceImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Import Identifier", capitalizeFirst(err.Error())+".")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), volumeID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), volumeID)...)
	if projectID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	}
	if region != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), region)...)
	}
}

func parseVolumeRecoveryServiceImportID(id string) (region, projectID, volumeID string, err error) {
	format := fmt.Errorf("expected <volume_id>, <project_id>/<volume_id> or <region>/<project_id>/<volume_id>, got %q", id)
	parts := strings.Split(strings.TrimSpace(id), "/")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return "", "", "", fmt.Errorf("%w: every segment must be non-empty", format)
		}
	}
	switch len(parts) {
	case 1:
		volumeID = parts[0]
	case 2:
		projectID, volumeID = parts[0], parts[1]
	case 3:
		region, projectID, volumeID = parts[0], parts[1], parts[2]
	default:
		return "", "", "", format
	}
	if !volumeIDPattern.MatchString(volumeID) {
		return "", "", "", fmt.Errorf("%w: %q is not a volume ID (a UUID)", format, volumeID)
	}
	return region, projectID, volumeID, nil
}

// target resolves the volume a resource acts on: its own region and
// project_id, or the provider's when unset. Both are recorded in state, so a
// later change to the provider's settings never retargets the resource.
func (r *volumeRecoveryServiceResource) target(m *volumeRecoveryServiceModel, diags *diag.Diagnostics) (volumeTarget, bool) {
	region := m.Region.ValueString()
	if region == "" {
		region = r.config.Region
	}
	if region == "" {
		diags.AddAttributeError(path.Root("region"), "Missing region",
			"Set region on this resource, or on the provider configuration, or use the CLEURA_REGION environment variable.")
		return volumeTarget{}, false
	}
	projectID, err := resolveProjectID(r.config, m.ProjectID.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("project_id"), "Missing Cleura project_id", capitalizeFirst(err.Error())+".")
		return volumeTarget{}, false
	}
	volumeID, err := uuid.Parse(m.VolumeID.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("volume_id"), "Invalid volume ID",
			fmt.Sprintf("%q is not a volume ID (a UUID).", m.VolumeID.ValueString()))
		return volumeTarget{}, false
	}
	return volumeTarget{region: region, projectID: projectID, volumeID: volumeID}, true
}

func recoverySettings(m *volumeRecoveryServiceModel) *api.CommonRecoverServiceRequest {
	return &api.CommonRecoverServiceRequest{
		RetentionDays: int(m.RetentionDays.ValueInt64()),
		Immutable:     m.Immutable.ValueBool(),
	}
}

func setRecoveryServiceState(m *volumeRecoveryServiceModel, t volumeTarget, v *api.OpenStackBlockStorageVolume) {
	s := v.DisasterRecoverService
	m.ID = m.VolumeID
	m.Region = types.StringValue(t.region)
	m.ProjectID = types.StringValue(t.projectID)
	m.RetentionDays = types.Int64Value(int64(s.RetentionDays))
	m.Immutable = types.BoolValue(s.Immutable)
	m.StartedAt = types.StringValue(s.Started.UTC().Format(time.RFC3339))
}

// getVolume reads a volume. A volume that doesn't exist returns found=false.
func getVolume(ctx context.Context, cfg *ProviderConfig, t volumeTarget) (*api.OpenStackBlockStorageVolume, bool, error) {
	resp, err := cfg.Client.OpenStackBlockStorageGetVolume(ctx, t.region, t.projectID, t.volumeID)
	if err != nil {
		return nil, false, fmt.Errorf("reading volume %s: %w", t.volumeID, err)
	}
	var v api.OpenStackBlockStorageVolume
	if err := decodeJSON(resp, &v); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading volume %s: %w", t.volumeID, err)
	}
	return &v, true, nil
}

// setRecoveryService switches a volume's Recovery service on, or changes its
// settings, or switches it off when settings is nil.
//
// The body carries recover_service and nothing else. The volume's name and
// description belong to whatever manages the volume, typically the OpenStack
// provider, and the generated request type can't send the explicit null that
// switches the service off.
func setRecoveryService(ctx context.Context, cfg *ProviderConfig, t volumeTarget, settings *api.CommonRecoverServiceRequest) (*api.OpenStackBlockStorageVolume, error) {
	body, err := json.Marshal(map[string]any{"recover_service": settings})
	if err != nil {
		return nil, err
	}
	resp, err := cfg.Client.OpenStackBlockStorageEditVolumeWithBody(ctx, t.region, t.projectID, t.volumeID,
		"application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var v api.OpenStackBlockStorageVolume
	if err := decodeJSON(resp, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// recoveryServiceOn reports whether a volume's Recovery service is on.
//
// API WORKAROUND: switching the service off takes a moment. The record stays
// on the volume as "stopping", then disappears. Only "active" counts as on;
// "stopping" and "stopped" count as off. See item 5 in
// .agent/volume-recovery-service-api-feedback.md.
func recoveryServiceOn(v *api.OpenStackBlockStorageVolume) bool {
	return v.DisasterRecoverService != nil &&
		v.DisasterRecoverService.Status == api.OpenStackBlockStorageVolumeDisasterRecoveryServiceStatusActive
}

// waitRecoveryServiceStopped waits until the volume no longer has a Recovery
// service record, or the volume is gone.
//
// API WORKAROUND: Cleura's API deletes a volume only once its Recovery service
// has stopped and its recovery snapshots are gone, and the record
// disappearing is the sign of that. Terraform deletes the volume straight
// after this resource, so Delete waits here until the record is gone. The
// OpenStack provider deletes through the OpenStack Block Storage API; whether
// that has the same rule hasn't been tested, and the wait is harmless either
// way. See item 1 in .agent/volume-recovery-service-api-feedback.md.
//
// "stopped" ends the wait too. It is one of the API's statuses, but it hasn't
// been seen in testing.
//
// Errors while polling are retried until the deadline, so a brief API hiccup
// doesn't fail a destroy that is otherwise on track.
func waitRecoveryServiceStopped(ctx context.Context, cfg *ProviderConfig, t volumeTarget) error {
	// A wall-clock deadline, so the timeout stays correct after the machine
	// sleeps.
	deadline := time.Now().Add(recoveryServiceStopTimeout)
	var lastErr error
	for {
		v, found, err := getVolume(ctx, cfg, t)
		switch {
		case err != nil:
			lastErr = err
		case !found, v.DisasterRecoverService == nil,
			v.DisasterRecoverService.Status == api.OpenStackBlockStorageVolumeDisasterRecoveryServiceStatusStopped:
			return nil
		default:
			lastErr = fmt.Errorf("the Recovery service on volume %s is still %s after %s",
				t.volumeID, v.DisasterRecoverService.Status, recoveryServiceStopTimeout)
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(recoveryServicePollInterval):
		}
	}
}

// volumeErrorDetail renders an API error for a diagnostic, naming the volume
// when the API says it doesn't exist.
func volumeErrorDetail(err error, t volumeTarget) string {
	if errors.Is(err, errNotFound) {
		return fmt.Sprintf("Volume %s was not found in project %s, region %s.", t.volumeID, t.projectID, t.region)
	}
	return err.Error()
}

func recoveryServiceStatusDetail(v *api.OpenStackBlockStorageVolume, t volumeTarget) string {
	status := "missing"
	if v.DisasterRecoverService != nil {
		status = string(v.DisasterRecoverService.Status)
	}
	return fmt.Sprintf("The API accepted the request, but the Recovery service on volume %s is %s, not active.",
		t.volumeID, status)
}
