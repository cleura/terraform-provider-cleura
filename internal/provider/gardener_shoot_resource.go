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

	reqBody := api.GardenerEditShootJSONRequestBody{
		AllowedCidrs:         &allowedCidrs,
		EnableHaControlPlane: data.EnableHaControlPlane.ValueBoolPointer(),
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

	resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Update worker groups after the shoot cluster updates has been reconciled.
	// Match by position/order (plan[i] ↔ state[i])—ordering is critical for state consistency.
	// Update overlapping pairs, delete excess state, create excess plan.

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

	nOverlap := len(workersListPlan)
	if len(workersListState) < nOverlap {
		nOverlap = len(workersListState)
	}

	// 1. Update workers matched by position (plan[i] ↔ state[i])
	for i := 0; i < nOverlap; i++ {
		planWorker := workersListPlan[i]
		stateWorker := workersListState[i]
		if planWorker.Equal(stateWorker) {
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
		existingName := stateWorker.Name.ValueString()
		updateResp, err := r.config.Client.GardenerUpdateWorkerWithBody(ctx, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), existingName, "application/json", bytes.NewReader(bodyBytes))
		if err != nil {
			resp.Diagnostics.AddError("Failed to update worker group", fmt.Sprintf("worker %q: %s", existingName, err.Error()))
			return
		}
		if updateResp.StatusCode < 200 || updateResp.StatusCode >= 300 {
			body, _ := io.ReadAll(updateResp.Body)
			updateResp.Body.Close()
			resp.Diagnostics.AddError("Failed to update worker group", fmt.Sprintf("worker %q: HTTP %d: %s", existingName, updateResp.StatusCode, string(body)))
			return
		}
		updateResp.Body.Close()
		resp.Diagnostics.Append(WaitForShootReconcile(ctx, r.config.Client, r.config.Cloud, r.config.Region, r.config.ProjectID, data.Name.ValueString(), false)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// 2. Delete excess state workers (indices nOverlap..len(workersListState)-1)
	for si := nOverlap; si < len(workersListState); si++ {
		name := workersListState[si].Name.ValueString()
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

	// 3. Create excess plan workers (indices nOverlap..len(workersListPlan)-1)
	for pi := nOverlap; pi < len(workersListPlan); pi++ {
		worker := workersListPlan[pi]
		createBody, ok := workerToCreateWorker(ctx, worker, &resp.Diagnostics)
		if !ok {
			return
		}
		name := worker.Name.ValueString()
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

	// Refresh state from API so it reflects the applied changes (e.g. empty annotations/labels/taints)
	SetShootStateValues(ctx, r.config, nil, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
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
