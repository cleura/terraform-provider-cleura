package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/cleura/terraform-provider-cleura/api"
	"github.com/cleura/terraform-provider-cleura/cleura"
	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

var _ resource.Resource = (*GardenerShootResource)(nil)

var _ resource.ResourceWithModifyPlan = (*GardenerShootResource)(nil)
var _ resource.ResourceWithImportState = (*GardenerShootResource)(nil)

func NewGardenerShootResource() resource.Resource {
	return &GardenerShootResource{}
}

type GardenerShootResource struct {
	config *cleura.ProviderConfig
}

func (r *GardenerShootResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *GardenerShootResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_shoot"
}

func (r *GardenerShootResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_gardener_shoot.GardenerShootResourceSchema(ctx)
}

func (r *GardenerShootResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	var data resource_gardener_shoot.GardenerShootModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var workers []api.GardenerCreateShootWorker

	var workersList []resource_gardener_shoot.WorkersValue
	resp.Diagnostics.Append(data.ShootProvider.Workers.ElementsAs(ctx, &workersList, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	for _, worker := range workersList {
		workerGroup, ok := workerToCreateWorker(ctx, worker, &resp.Diagnostics)
		if !ok {
			return
		}
		workers = append(workers, workerGroup)
	}

	infraConfigValuable, diags := resource_gardener_shoot.InfrastructureConfigType{}.ValueFromObject(ctx, data.ShootProvider.InfrastructureConfig)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	infraConfig := infraConfigValuable.(resource_gardener_shoot.InfrastructureConfigValue)

	allowedCidrs := []string{}
	if !data.AllowedCidrs.IsNull() && !data.AllowedCidrs.IsUnknown() {
		resp.Diagnostics.Append(data.AllowedCidrs.ElementsAs(ctx, &allowedCidrs, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	var hibernationSchedulesList []resource_gardener_shoot.HibernationSchedulesValue
	if !data.HibernationSchedules.IsNull() && !data.HibernationSchedules.IsUnknown() {
		resp.Diagnostics.Append(data.HibernationSchedules.ElementsAs(ctx, &hibernationSchedulesList, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	var maintenanceAutoUpdate resource_gardener_shoot.AutoUpdateValue
	hasAutoUpdate := !data.Maintenance.AutoUpdate.IsNull() && !data.Maintenance.AutoUpdate.IsUnknown()
	if hasAutoUpdate {
		maintenanceAutoUpdateValuable, diags := resource_gardener_shoot.AutoUpdateType{}.ValueFromObject(ctx, data.Maintenance.AutoUpdate)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		maintenanceAutoUpdate = maintenanceAutoUpdateValuable.(resource_gardener_shoot.AutoUpdateValue)
	}
	var maintenanceTimeWindow resource_gardener_shoot.TimeWindowValue
	hasTimeWindow := !data.Maintenance.TimeWindow.IsNull() && !data.Maintenance.TimeWindow.IsUnknown()
	if hasTimeWindow {
		maintenanceTimeWindowValuable, diags := resource_gardener_shoot.TimeWindowType{}.ValueFromObject(ctx, data.Maintenance.TimeWindow)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		maintenanceTimeWindow = maintenanceTimeWindowValuable.(resource_gardener_shoot.TimeWindowValue)
	}

	var hibernationSchedules []api.GardenerCreateShootHibernationSchedule
	for _, hs := range hibernationSchedulesList {
		hibernationSchedules = append(hibernationSchedules, api.GardenerCreateShootHibernationSchedule{
			Start: hs.Start.ValueString(),
			End:   hs.End.ValueString(),
		})
	}

	var hibernationSchedulesPtr *[]api.GardenerCreateShootHibernationSchedule
	if len(hibernationSchedules) > 0 {
		hibernationSchedulesPtr = &hibernationSchedules
	}

	var maintenancePtr *api.GardenerCreateShootMaintenance
	if hasAutoUpdate || hasTimeWindow {
		m := &api.GardenerCreateShootMaintenance{}
		if hasAutoUpdate {
			m.AutoUpdate = &api.GardenerCreateShootAutoUpdate{
				KubernetesVersion:   maintenanceAutoUpdate.KubernetesVersion.ValueBoolPointer(),
				MachineImageVersion: maintenanceAutoUpdate.MachineImageVersion.ValueBoolPointer(),
			}
		}
		if hasTimeWindow {
			m.TimeWindow = &api.GardenerTimeWindow{
				Begin: maintenanceTimeWindow.Begin.ValueString(),
				End:   maintenanceTimeWindow.End.ValueString(),
			}
		}
		maintenancePtr = m
	}

	reqBody := api.GardenerCreateShootShoot{
		AllowedCidrs:         &allowedCidrs,
		Name:                 data.Name.ValueString(),
		EnableHaControlPlane: data.EnableHaControlPlane.ValueBoolPointer(),
		KubernetesVersion:    data.KubernetesVersion.ValueString(),
		ShootProvider: api.GardenerCreateShootProvider{
			InfrastructureConfig: api.GardenerCreateShootInfrastructure{
				FloatingPoolName:   infraConfig.FloatingPoolName.ValueString(),
				NetworkId:          stringPtrOrNil(infraConfig.NetworkId),
				RouterId:           stringPtrOrNil(infraConfig.RouterId),
				WorkersNetworkCidr: stringPtrOrNil(infraConfig.WorkersNetworkCidr),
			},
			LoadBalancerProvider: data.ShootProvider.LoadBalancerProvider.ValueString(),
			Workers:              workers,
		},
		HibernationSchedules: hibernationSchedulesPtr,
		Maintenance:          maintenancePtr,
	}
	response, err := r.config.Client.GardenerCreateShoot(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, reqBody)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Gardener cluster", err.Error())
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

	var shootCluster api.GardenerShootShoot
	if err := json.Unmarshal(body, &shootCluster); err != nil {
		resp.Diagnostics.AddError("Failed to unmarshal response", err.Error())
		return
	}

	SetShootStateValues(ctx, r.config, &shootCluster, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The cluster now exists remotely. Persist state before waiting for
	// reconciliation so that a reconcile failure or timeout leaves a tracked
	// (tainted) resource that `terraform destroy`/re-apply can clean up,
	// instead of an orphaned cluster missing from state.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *GardenerShootResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	var data resource_gardener_shoot.GardenerShootModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	SetShootStateValues(ctx, r.config, nil, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *GardenerShootResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	var data resource_gardener_shoot.GardenerShootModel
	var state resource_gardener_shoot.GardenerShootModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	allowedCidrs := []string{}
	if !data.AllowedCidrs.IsNull() && !data.AllowedCidrs.IsUnknown() {
		resp.Diagnostics.Append(data.AllowedCidrs.ElementsAs(ctx, &allowedCidrs, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	var hibernationSchedulesList []resource_gardener_shoot.HibernationSchedulesValue
	if !data.HibernationSchedules.IsNull() && !data.HibernationSchedules.IsUnknown() {
		resp.Diagnostics.Append(data.HibernationSchedules.ElementsAs(ctx, &hibernationSchedulesList, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	maintenanceAutoUpdateValuable, diags := resource_gardener_shoot.AutoUpdateType{}.ValueFromObject(ctx, data.Maintenance.AutoUpdate)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	maintenanceAutoUpdate := maintenanceAutoUpdateValuable.(resource_gardener_shoot.AutoUpdateValue)

	maintenanceTimeWindowValuable, diags := resource_gardener_shoot.TimeWindowType{}.ValueFromObject(ctx, data.Maintenance.TimeWindow)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	maintenanceTimeWindow := maintenanceTimeWindowValuable.(resource_gardener_shoot.TimeWindowValue)

	var hibernationSchedules []api.GardenerEditShootHibernationSchedule
	for _, hs := range hibernationSchedulesList {
		hibernationSchedules = append(hibernationSchedules, api.GardenerEditShootHibernationSchedule{
			Start: hs.Start.ValueStringPointer(),
			End:   hs.End.ValueStringPointer(),
		})
	}

	var hibernationSchedulesPtr *[]api.GardenerEditShootHibernationSchedule
	if len(hibernationSchedules) > 0 {
		hibernationSchedulesPtr = &hibernationSchedules
	}

	// Only send enable_ha_control_plane when it actually changes. The Cleura API
	// returns 409 if asked to enable HA on a shoot where it is already enabled, and
	// disabling HA is handled as a replacement in ModifyPlan — so re-sending the
	// unchanged value on an in-place update would always fail once HA is on.
	var enableHaControlPlane *bool
	if !data.EnableHaControlPlane.Equal(state.EnableHaControlPlane) {
		enableHaControlPlane = data.EnableHaControlPlane.ValueBoolPointer()
	}

	reqBody := api.GardenerEditShootJSONRequestBody{
		AllowedCidrs:         &allowedCidrs,
		EnableHaControlPlane: enableHaControlPlane,
		HibernationSchedules: hibernationSchedulesPtr,
		Kubernetes:           data.KubernetesVersion.ValueStringPointer(),
		Maintenance: &api.GardenerEditShootMaintenance{
			AutoUpdate: api.GardenerEditShootAutoUpdate{
				KubernetesVersion:   maintenanceAutoUpdate.KubernetesVersion.ValueBool(),
				MachineImageVersion: maintenanceAutoUpdate.MachineImageVersion.ValueBool(),
			},
			TimeWindow: api.GardenerTimeWindow{
				Begin: maintenanceTimeWindow.Begin.ValueString(),
				End:   maintenanceTimeWindow.End.ValueString(),
			},
		},
	}

	response, err := r.config.Client.GardenerEditShoot(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), reqBody)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Gardener cluster", err.Error())
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

	// The shoot edit above and the worker operations below all mutate the cluster
	// remotely. Refresh from the API and persist state on every exit path (success
	// or failure) so a mid-sequence error doesn't leave Terraform state out of sync
	// with the changes already applied.
	defer func() {
		var refreshDiags diag.Diagnostics
		SetShootStateValues(ctx, r.config, nil, &data, &refreshDiags)
		if refreshDiags.HasError() {
			// Couldn't read the cluster back; leave existing state untouched rather
			// than overwrite it with unverified values.
			resp.Diagnostics.Append(refreshDiags...)
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	}()

	resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Reconcile worker groups after the shoot-level update. A worker group is
	// identified by its (immutable) name, so match plan↔state by name: update
	// changed groups, delete groups removed from config, create new ones. Matching
	// by name (not list position) keeps the right group targeted when groups are
	// reordered or one is removed from the middle.

	var workersListPlan []resource_gardener_shoot.WorkersValue
	resp.Diagnostics.Append(data.ShootProvider.Workers.ElementsAs(ctx, &workersListPlan, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var workersListState []resource_gardener_shoot.WorkersValue
	if !state.ShootProvider.Workers.IsNull() && !state.ShootProvider.Workers.IsUnknown() {
		resp.Diagnostics.Append(state.ShootProvider.Workers.ElementsAs(ctx, &workersListState, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	stateWorkersByName := WorkersListToMap(workersListState)
	planWorkerNames := make(map[string]struct{}, len(workersListPlan))
	for _, w := range workersListPlan {
		planWorkerNames[w.Name.ValueString()] = struct{}{}
	}

	// 1. Update worker groups present in both plan and state that have changed.
	for _, planWorker := range workersListPlan {
		name := planWorker.Name.ValueString()
		stateWorker, exists := stateWorkersByName[name]
		if !exists || planWorker.Equal(stateWorker) {
			continue
		}
		updateBody, ok := workerToEditWorker(ctx, planWorker, &resp.Diagnostics)
		if !ok {
			return
		}
		bodyBytes, err := workerUpdateBodyWithExplicitEmptyArrays(updateBody)
		if err != nil {
			resp.Diagnostics.AddError("Failed to build worker update request", err.Error())
			return
		}
		updateResp, err := r.config.Client.GardenerUpdateWorkerWithBody(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), name, "application/json", bytes.NewReader(bodyBytes))
		if err != nil {
			resp.Diagnostics.AddError("Failed to update worker group", fmt.Sprintf("worker %q: %s", name, err.Error()))
			return
		}
		if updateResp.StatusCode < 200 || updateResp.StatusCode >= 300 {
			body, _ := io.ReadAll(updateResp.Body)
			updateResp.Body.Close()
			resp.Diagnostics.AddError("Failed to update worker group", fmt.Sprintf("worker %q: HTTP %d: %s", name, updateResp.StatusCode, string(body)))
			return
		}
		updateResp.Body.Close()
		resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// 2. Delete worker groups present in state but no longer in the plan.
	for _, stateWorker := range workersListState {
		name := stateWorker.Name.ValueString()
		if _, kept := planWorkerNames[name]; kept {
			continue
		}
		delResp, err := r.config.Client.GardenerDeleteWorker(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), name)
		if err != nil {
			resp.Diagnostics.AddError("Failed to delete worker group", fmt.Sprintf("worker %q: %s", name, err.Error()))
			return
		}
		if delResp.StatusCode < 200 || delResp.StatusCode >= 300 {
			body, _ := io.ReadAll(delResp.Body)
			delResp.Body.Close()
			resp.Diagnostics.AddError("Failed to delete worker group", fmt.Sprintf("worker %q: HTTP %d: %s", name, delResp.StatusCode, string(body)))
			return
		}
		delResp.Body.Close()
		resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// 3. Create worker groups present in the plan but not yet in state.
	for _, planWorker := range workersListPlan {
		name := planWorker.Name.ValueString()
		if _, exists := stateWorkersByName[name]; exists {
			continue
		}
		createBody, ok := workerToCreateWorker(ctx, planWorker, &resp.Diagnostics)
		if !ok {
			return
		}
		createResp, err := r.config.Client.GardenerCreateWorker(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), createBody)
		if err != nil {
			resp.Diagnostics.AddError("Failed to create worker group", fmt.Sprintf("worker %q: %s", name, err.Error()))
			return
		}
		if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
			body, _ := io.ReadAll(createResp.Body)
			createResp.Body.Close()
			resp.Diagnostics.AddError("Failed to create worker group", fmt.Sprintf("worker %q: HTTP %d: %s", name, createResp.StatusCode, string(body)))
			return
		}
		createResp.Body.Close()
		resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// State is refreshed from the API and persisted by the deferred refresh above
	// (covering both this success path and every early-return error path).
}

func (r *GardenerShootResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	shootName := strings.TrimSpace(req.ID)
	if shootName == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			"Expected the shoot name. cloud, region, and project_id are taken from the provider configuration.",
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), shootName)...)
}

func (r *GardenerShootResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	var data resource_gardener_shoot.GardenerShootModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	response, err := r.config.Client.GardenerDeleteShoot(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete Gardener cluster", err.Error())
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

	resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), true)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *GardenerShootResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !require(r.config, &resp.Diagnostics, true) {
		return
	}

	// Destroy: plan is null, nothing to modify
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan resource_gardener_shoot.GardenerShootModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state resource_gardener_shoot.GardenerShootModel

	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}

		// UPDATE: Name change requires replacement
		if !plan.Name.Equal(state.Name) {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("name"))
			resp.Diagnostics.AddWarning("Updating immutable field", "Gardener Shoot clusters do not allow updating the name. This requires the cluster and its worker groups to be recreated, including removing all cluster data!")
		}

		if state.EnableHaControlPlane.Equal(types.BoolValue(true)) && plan.EnableHaControlPlane.Equal(types.BoolValue(false)) {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("enable_ha_control_plane"))
			resp.Diagnostics.AddWarning("Updating immutable field", "Gardener Shoot clusters do not allow disabling Control Plane High-Availability after it has been enabled. This requires the cluster and its worker groups to be recreated, including removing all cluster data!")
		}
	}

	// Handle CloudProfileName: Computed-only — copy from state to avoid perpetual drift.
	if plan.CloudProfileName.IsUnknown() || plan.CloudProfileName.IsNull() {
		if !req.State.Raw.IsNull() && !state.CloudProfileName.IsUnknown() {
			plan.CloudProfileName = state.CloudProfileName
		}
	}

	// Handle EnableHaControlPlane: Optional+Computed without schema default
	if plan.EnableHaControlPlane.IsUnknown() || plan.EnableHaControlPlane.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			plan.EnableHaControlPlane = basetypes.NewBoolUnknown()
		} else if !state.EnableHaControlPlane.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			plan.EnableHaControlPlane = state.EnableHaControlPlane
		}
	}

	// Handle Maintenance: Optional+Computed without schema default
	if plan.Maintenance.IsUnknown() || plan.Maintenance.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			plan.Maintenance = resource_gardener_shoot.NewMaintenanceValueUnknown()
		} else if !state.Maintenance.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			plan.Maintenance = state.Maintenance
		}
	}

	// Handle HibernationSchedules: Optional+Computed without schema default
	if plan.HibernationSchedules.IsUnknown() || plan.HibernationSchedules.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			plan.HibernationSchedules = basetypes.NewListNull(resource_gardener_shoot.NewHibernationSchedulesValueNull().Type(ctx))
		} else if !state.HibernationSchedules.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			plan.HibernationSchedules = state.HibernationSchedules
		}
	}

	// Handle AllowedCidrs: Optional+Computed without schema default
	if plan.AllowedCidrs.IsUnknown() || plan.AllowedCidrs.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			plan.AllowedCidrs = basetypes.NewListUnknown(types.StringType)
		} else if !state.AllowedCidrs.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			plan.AllowedCidrs = state.AllowedCidrs
		}
	}

	// Read the planned and state InfrastructureConfig into more usable datatypes
	infraConfigValuable, diags := resource_gardener_shoot.InfrastructureConfigType{}.ValueFromObject(ctx, plan.ShootProvider.InfrastructureConfig)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	infraConfigPlan := infraConfigValuable.(resource_gardener_shoot.InfrastructureConfigValue)

	// Load the InfrastructureConfig from state if it exists
	infraConfigState := resource_gardener_shoot.NewInfrastructureConfigValueNull()
	if !state.ShootProvider.InfrastructureConfig.IsNull() {
		infraConfigValuable, diags = resource_gardener_shoot.InfrastructureConfigType{}.ValueFromObject(ctx, state.ShootProvider.InfrastructureConfig)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		infraConfigState = infraConfigValuable.(resource_gardener_shoot.InfrastructureConfigValue)
	}

	// Handle NetworkId: Optional+Computed without schema default
	if infraConfigPlan.NetworkId.IsUnknown() || infraConfigPlan.NetworkId.IsNull() {
		if req.State.Raw.IsNull() {
			infraConfigPlan.NetworkId = basetypes.NewStringUnknown()
		} else if !infraConfigState.NetworkId.IsUnknown() {
			infraConfigPlan.NetworkId = infraConfigState.NetworkId
		}
	}

	// Handle RouterId: Optional+Computed without schema default
	if infraConfigPlan.RouterId.IsUnknown() || infraConfigPlan.RouterId.IsNull() {
		if req.State.Raw.IsNull() {
			infraConfigPlan.RouterId = basetypes.NewStringUnknown()
		} else if !infraConfigState.RouterId.IsUnknown() {
			infraConfigPlan.RouterId = infraConfigState.RouterId
		}
	}

	// Handle WorkersNetworkCidr: Optional+Computed without schema default
	if infraConfigPlan.WorkersNetworkCidr.IsUnknown() || infraConfigPlan.WorkersNetworkCidr.IsNull() {
		if req.State.Raw.IsNull() {
			infraConfigPlan.WorkersNetworkCidr = basetypes.NewStringUnknown()
		} else if !infraConfigState.WorkersNetworkCidr.IsUnknown() {
			infraConfigPlan.WorkersNetworkCidr = infraConfigState.WorkersNetworkCidr
		}
	}

	// Convert the potentially updated InfrastructureConfig back into basetypes.ObjectValue, and update the plan
	infraConfigObject, diags := infraConfigPlan.ToObjectValue(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ShootProvider.InfrastructureConfig = infraConfigObject

	// Handle Workers: Optional+Computed without schema default
	var workersListPlan []resource_gardener_shoot.WorkersValue
	resp.Diagnostics.Append(plan.ShootProvider.Workers.ElementsAs(ctx, &workersListPlan, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var workersListState []resource_gardener_shoot.WorkersValue
	if !state.ShootProvider.Workers.IsNull() {
		resp.Diagnostics.Append(state.ShootProvider.Workers.ElementsAs(ctx, &workersListState, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	if !req.State.Raw.IsNull() && len(workersListPlan) != len(workersListState) {
		resp.Diagnostics.AddWarning(
			"Changing number of worker groups",
			"Adding or removing worker groups may cause temporary downtime or over-provisioning. Consider keeping the same number of groups and updating config in place.",
		)
	}

	workersMapState := WorkersListToMap(workersListState)

	for i, workerPlan := range workersListPlan {
		workerState, exists := workersMapState[workerPlan.Name.ValueString()]

		if workerPlan.Maximum.IsUnknown() || workerPlan.Maximum.IsNull() {
			if req.State.Raw.IsNull() {
				workersListPlan[i].Maximum = basetypes.NewInt64Unknown()
			} else if exists && !workerState.Maximum.IsUnknown() {
				workersListPlan[i].Maximum = workerState.Maximum
			}
		}

		if workerPlan.Minimum.IsUnknown() || workerPlan.Minimum.IsNull() {
			if req.State.Raw.IsNull() {
				workersListPlan[i].Minimum = basetypes.NewInt64Unknown()
			} else if exists && !workerState.Minimum.IsUnknown() {
				workersListPlan[i].Minimum = workerState.Minimum
			}
		}

		if workerPlan.MaxSurge.IsUnknown() || workerPlan.MaxSurge.IsNull() {
			if req.State.Raw.IsNull() {
				workersListPlan[i].MaxSurge = basetypes.NewInt64Unknown()
			} else if exists && !workerState.MaxSurge.IsUnknown() {
				workersListPlan[i].MaxSurge = workerState.MaxSurge
			}
		}

		if workerPlan.Taints.IsUnknown() || workerPlan.Taints.IsNull() {
			// Not managed by Terraform: send empty array to remove server-side values
			emptyTaints, d := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.TaintsValue{}.Type(ctx), []resource_gardener_shoot.TaintsValue{})
			resp.Diagnostics.Append(d...)
			if !resp.Diagnostics.HasError() {
				workersListPlan[i].Taints = emptyTaints
			}
		}

		if workerPlan.Zones.IsUnknown() || workerPlan.Zones.IsNull() {
			if req.State.Raw.IsNull() {
				workersListPlan[i].Zones = basetypes.NewListUnknown(basetypes.StringType{})
			} else if exists && !workerState.Zones.IsUnknown() {
				workersListPlan[i].Zones = workerState.Zones
			}
		}

		if workerPlan.Annotations.IsUnknown() || workerPlan.Annotations.IsNull() {
			// Not managed by Terraform: send empty array to remove server-side values
			emptyAnnotations, d := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.AnnotationsValue{}.Type(ctx), []resource_gardener_shoot.AnnotationsValue{})
			resp.Diagnostics.Append(d...)
			if !resp.Diagnostics.HasError() {
				workersListPlan[i].Annotations = emptyAnnotations
			}
		}

		if workerPlan.Labels.IsUnknown() || workerPlan.Labels.IsNull() {
			// Not managed by Terraform: send empty array to remove server-side values
			emptyLabels, d := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.LabelsValue{}.Type(ctx), []resource_gardener_shoot.LabelsValue{})
			resp.Diagnostics.Append(d...)
			if !resp.Diagnostics.HasError() {
				workersListPlan[i].Labels = emptyLabels
			}
		}
	}

	workersListValue, diags := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.WorkersValue{}.Type(ctx), workersListPlan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ShootProvider.Workers = workersListValue

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}
