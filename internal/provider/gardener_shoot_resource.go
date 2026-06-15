package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	api "github.com/cleura/terraform-provider-cleura/api"
	cleura "github.com/cleura/terraform-provider-cleura/client"
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
	r.config = providerConfigFromResource(ctx, req, resp)
}

func (r *GardenerShootResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_shoot"
}

func (r *GardenerShootResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_gardener_shoot.GardenerShootResourceSchema(ctx)
}

func WorkersListToMap(workers []resource_gardener_shoot.WorkersValue) map[string]resource_gardener_shoot.WorkersValue {
	res := make(map[string]resource_gardener_shoot.WorkersValue, len(workers))

	for _, worker := range workers {
		res[worker.Name.ValueString()] = worker
	}

	return res
}

// workerUpdateBodyWithExplicitEmptyArrays marshals the worker to JSON and ensures
// labels, annotations, and taints are explicitly [] when empty (omitempty may omit them,
// but the API requires empty array to remove existing values).
func workerUpdateBodyWithExplicitEmptyArrays(body api.GardenerEditShootWorker) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if body.Labels != nil && len(*body.Labels) == 0 {
		m["labels"] = []interface{}{}
	}
	if body.Annotations != nil && len(*body.Annotations) == 0 {
		m["annotations"] = []interface{}{}
	}
	if body.Taints != nil && len(*body.Taints) == 0 {
		m["taints"] = []interface{}{}
	}
	return json.Marshal(m)
}

// workerToEditWorker converts Terraform WorkersValue to API GardenerEditShootWorker.
func workerToEditWorker(ctx context.Context, worker resource_gardener_shoot.WorkersValue, diag *diag.Diagnostics) (api.GardenerEditShootWorker, bool) {
	machineValueable, d := resource_gardener_shoot.MachineType{}.ValueFromObject(ctx, worker.Machine)
	diag.Append(d...)
	if diag.HasError() {
		return api.GardenerEditShootWorker{}, false
	}
	machine := machineValueable.(resource_gardener_shoot.MachineValue)

	var annotations []api.GardenerAnnotation
	if !worker.Annotations.IsNull() && !worker.Annotations.IsUnknown() {
		var annotationsValues []resource_gardener_shoot.AnnotationsValue
		diag.Append(worker.Annotations.ElementsAs(ctx, &annotationsValues, false)...)
		if diag.HasError() {
			return api.GardenerEditShootWorker{}, false
		}
		for _, a := range annotationsValues {
			annotations = append(annotations, api.GardenerAnnotation{
				Key:   a.Key.ValueString(),
				Value: a.Value.ValueString(),
			})
		}
	}

	var labels []api.GardenerLabel
	if !worker.Labels.IsNull() && !worker.Labels.IsUnknown() {
		var labelsValues []resource_gardener_shoot.LabelsValue
		diag.Append(worker.Labels.ElementsAs(ctx, &labelsValues, false)...)
		if diag.HasError() {
			return api.GardenerEditShootWorker{}, false
		}
		for _, l := range labelsValues {
			labels = append(labels, api.GardenerLabel{
				Key:   l.Key.ValueString(),
				Value: l.Value.ValueString(),
			})
		}
	}

	var taints []api.GardenerEditShootNodeTaint
	if !worker.Taints.IsNull() && !worker.Taints.IsUnknown() {
		var taintsValues []resource_gardener_shoot.TaintsValue
		diag.Append(worker.Taints.ElementsAs(ctx, &taintsValues, false)...)
		if diag.HasError() {
			return api.GardenerEditShootWorker{}, false
		}
		for _, t := range taintsValues {
			taints = append(taints, api.GardenerEditShootNodeTaint{
				Key:    t.Key.ValueString(),
				Value:  t.Value.ValueStringPointer(),
				Effect: api.GardenerShootWorkerTaintEffect(t.Effect.ValueString()),
			})
		}
	}

	var zones []string
	if !worker.Zones.IsNull() && !worker.Zones.IsUnknown() {
		diag.Append(worker.Zones.ElementsAs(ctx, &zones, false)...)
		if diag.HasError() {
			return api.GardenerEditShootWorker{}, false
		}
	}

	var maxSurge, maximum, minimum *int
	if !worker.MaxSurge.IsNull() && !worker.MaxSurge.IsUnknown() {
		v := int(worker.MaxSurge.ValueInt64())
		maxSurge = &v
	}
	if !worker.Maximum.IsNull() && !worker.Maximum.IsUnknown() {
		v := int(worker.Maximum.ValueInt64())
		maximum = &v
	}
	if !worker.Minimum.IsNull() && !worker.Minimum.IsUnknown() {
		v := int(worker.Minimum.ValueInt64())
		minimum = &v
	}

	return api.GardenerEditShootWorker{
		Name:        worker.Name.ValueStringPointer(),
		Annotations: &annotations,
		Labels:      &labels,
		Machine: &api.GardenerMachine{
			Type:         machine.MachineType.ValueStringPointer(),
			ImageName:    machine.ImageName.ValueStringPointer(),
			ImageVersion: machine.ImageVersion.ValueStringPointer(),
		},
		MaxSurge:   maxSurge,
		Maximum:    maximum,
		Minimum:    minimum,
		Taints:     &taints,
		VolumeSize: worker.VolumeSize.ValueStringPointer(),
		Zones:      &zones,
	}, true
}

// workerToCreateWorker converts Terraform WorkersValue to API GardenerCreateShootWorker.
func workerToCreateWorker(ctx context.Context, worker resource_gardener_shoot.WorkersValue, diag *diag.Diagnostics) (api.GardenerCreateShootWorker, bool) {
	edit, ok := workerToEditWorker(ctx, worker, diag)
	if !ok {
		return api.GardenerCreateShootWorker{}, false
	}

	createTaints := make([]api.GardenerCreateShootNodeTaint, len(*edit.Taints))

	for i, taint := range *edit.Taints {
		createTaints[i] = api.GardenerCreateShootNodeTaint{
			Key:    taint.Key,
			Value:  *taint.Value,
			Effect: taint.Effect,
		}
	}

	return api.GardenerCreateShootWorker{
		Name:        edit.Name,
		Annotations: edit.Annotations,
		Labels:      edit.Labels,
		Machine:     *edit.Machine,
		MaxSurge:    edit.MaxSurge,
		Maximum:     edit.Maximum,
		Minimum:     edit.Minimum,
		Taints:      &createTaints,
		VolumeSize:  *edit.VolumeSize,
		Zones:       edit.Zones,
	}, true
}

func stringPtrOrNil(v basetypes.StringValue) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	if s == "" {
		return nil
	}
	return &s
}

func (r *GardenerShootResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !requireProviderConfig(r.config, &resp.Diagnostics, true) {
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

	// // Handle HibernationSchedules: Optional+Computed without schema default
	if plan.HibernationSchedules.IsUnknown() || plan.HibernationSchedules.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			plan.HibernationSchedules = basetypes.NewListNull(resource_gardener_shoot.NewHibernationSchedulesValueNull().Type(ctx))
		} else if !state.HibernationSchedules.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			plan.HibernationSchedules = state.HibernationSchedules
		}
	}

	// // Handle HibernationSchedules: Optional+Computed without schema default
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
			// CREATE: apply default value when not configured
			infraConfigPlan.NetworkId = basetypes.NewStringUnknown()
		} else if !infraConfigState.NetworkId.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			infraConfigPlan.NetworkId = infraConfigState.NetworkId
		}
	}

	// Handle RouterId: Optional+Computed without schema default
	if infraConfigPlan.RouterId.IsUnknown() || infraConfigPlan.RouterId.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			infraConfigPlan.RouterId = basetypes.NewStringUnknown()
		} else if !infraConfigState.RouterId.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
			infraConfigPlan.RouterId = infraConfigState.RouterId
		}
	}

	// Handle WorkersNetworkCidr: Optional+Computed without schema default
	if infraConfigPlan.WorkersNetworkCidr.IsUnknown() || infraConfigPlan.WorkersNetworkCidr.IsNull() {
		if req.State.Raw.IsNull() {
			// CREATE: apply default value when not configured
			infraConfigPlan.WorkersNetworkCidr = basetypes.NewStringUnknown()
		} else if !infraConfigState.WorkersNetworkCidr.IsUnknown() {
			// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
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

		// Handle Maximum: UseStateForUnknown - copy from state to avoid "known after apply"
		if workerPlan.Maximum.IsUnknown() || workerPlan.Maximum.IsNull() {
			if req.State.Raw.IsNull() {
				// CREATE: apply default value when not configured
				workersListPlan[i].Maximum = basetypes.NewInt64Unknown()
			} else if exists && !workerState.Maximum.IsUnknown() {
				// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
				workersListPlan[i].Maximum = workerState.Maximum
			}
		}

		// Handle Minimum: UseStateForUnknown - copy from state to avoid "known after apply"
		if workerPlan.Minimum.IsUnknown() || workerPlan.Minimum.IsNull() {
			if req.State.Raw.IsNull() {
				// CREATE: apply default value when not configured
				workersListPlan[i].Minimum = basetypes.NewInt64Unknown()
			} else if exists && !workerState.Minimum.IsUnknown() {
				// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
				workersListPlan[i].Minimum = workerState.Minimum
			}
		}

		// Handle MaxSurge: UseStateForUnknown - copy from state to avoid "known after apply"
		if workerPlan.MaxSurge.IsUnknown() || workerPlan.MaxSurge.IsNull() {
			if req.State.Raw.IsNull() {
				// CREATE: apply default value when not configured
				workersListPlan[i].MaxSurge = basetypes.NewInt64Unknown()
			} else if exists && !workerState.MaxSurge.IsUnknown() {
				// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
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
				// CREATE: apply default value when not configured
				workersListPlan[i].Zones = basetypes.NewListUnknown(basetypes.StringType{})
			} else if exists && !workerState.Zones.IsUnknown() {
				// UPDATE: UseStateForUnknown - copy from state to avoid "known after apply"
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

func (r *GardenerShootResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !requireProviderConfig(r.config, &resp.Diagnostics, true) {
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
	if !requireProviderConfig(r.config, &resp.Diagnostics, true) {
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
	if !requireProviderConfig(r.config, &resp.Diagnostics, true) {
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
	if !requireProviderConfig(r.config, &resp.Diagnostics, true) {
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
	if !requireProviderConfig(r.config, &resp.Diagnostics, true) {
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

func SetShootStateValues(ctx context.Context, cfg *cleura.ProviderConfig, shootCluster *api.GardenerShootShoot, data *resource_gardener_shoot.GardenerShootModel, diag *diag.Diagnostics) {
	// Fetch from API when shootCluster not provided (e.g. Read, Update after worker changes)
	if shootCluster == nil {
		if cfg == nil || cfg.Client == nil {
			diag.AddError("Missing provider config", "SetShootStateValues requires a configured Cleura provider")
			return
		}
		resp, err := cfg.Client.GardenerGetShoot(ctx, cfg.Cloud, cfg.Region, cfg.ProjectID, data.Name.ValueString())
		if err != nil {
			diag.AddError("Failed to get Gardener cluster", err.Error())
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			diag.AddError("Failed to read response body", err.Error())
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			diag.AddError(fmt.Sprintf("API error %d", resp.StatusCode), string(body))
			return
		}
		var fetched api.GardenerShootShoot
		if err := json.Unmarshal(body, &fetched); err != nil {
			diag.AddError("Failed to unmarshal response", err.Error())
			return
		}
		shootCluster = &fetched
	}

	// All values below are built from API response (data may be empty during import)

	// AllowedCidrs
	if shootCluster.AllowedCidrs != nil && len(*shootCluster.AllowedCidrs) > 0 {
		allowedCidrsVal, diags := basetypes.NewListValueFrom(ctx, basetypes.StringType{}, *shootCluster.AllowedCidrs)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}
		data.AllowedCidrs = allowedCidrsVal
	} else {
		data.AllowedCidrs = basetypes.NewListNull(basetypes.StringType{})
	}

	// EnableHaControlPlane (ControlPlane with HighAvailability indicates HA is enabled)
	data.EnableHaControlPlane = basetypes.NewBoolValue(shootCluster.ControlPlane != nil)

	// CloudProfileName
	data.CloudProfileName = basetypes.NewStringValue(shootCluster.CloudProfileName)

	// KubernetesVersion
	data.KubernetesVersion = basetypes.NewStringValue(shootCluster.Kubernetes.Version)

	// ShootProvider - build full object so state is Known (mutating fields leaves null/unknown state)
	apiInfra := shootCluster.ShootProvider.InfrastructureConfig

	var networkId, routerId basetypes.StringValue
	if apiInfra.Networks.Id != nil {
		networkId = basetypes.NewStringValue(*apiInfra.Networks.Id)
	} else {
		networkId = basetypes.NewStringNull()
	}
	if apiInfra.Networks.Router != nil && apiInfra.Networks.Router.Id != nil {
		routerId = basetypes.NewStringValue(*apiInfra.Networks.Router.Id)
	} else {
		routerId = basetypes.NewStringNull()
	}

	infraConfigObj, diags := types.ObjectValue(
		resource_gardener_shoot.InfrastructureConfigValue{}.AttributeTypes(ctx),
		map[string]attr.Value{
			"floating_pool_name":   basetypes.NewStringValue(apiInfra.FloatingPoolName),
			"network_id":           networkId,
			"router_id":            routerId,
			"workers_network_cidr": basetypes.NewStringValue(apiInfra.Networks.Workers),
		},
	)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}

	loadBalancerProviderVal := basetypes.NewStringValue(shootCluster.ShootProvider.ControlPlaneConfig.LoadBalancerProvider)

	if shootCluster.Hibernation != nil {
		hibernationSchedules := []resource_gardener_shoot.HibernationSchedulesValue{}
		for _, schedule := range shootCluster.Hibernation.Schedules {
			hibernationSchedules = append(hibernationSchedules, resource_gardener_shoot.HibernationSchedulesValue{
				Start: basetypes.NewStringValue(*schedule.Start),
				End:   basetypes.NewStringValue(*schedule.End),
			})
		}
		data.HibernationSchedules, diags = basetypes.NewListValueFrom(ctx, resource_gardener_shoot.HibernationSchedulesValue{}.Type(ctx), hibernationSchedules)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}
	} else {
		data.HibernationSchedules = basetypes.NewListNull(resource_gardener_shoot.HibernationSchedulesValue{}.Type(ctx))
	}

	autoUpdateObj, diags := types.ObjectValue(
		resource_gardener_shoot.AutoUpdateValue{}.AttributeTypes(ctx),
		map[string]attr.Value{
			"kubernetes_version":    basetypes.NewBoolValue(shootCluster.Maintenance.AutoUpdate.KubernetesVersion),
			"machine_image_version": basetypes.NewBoolValue(shootCluster.Maintenance.AutoUpdate.MachineImageVersion),
		},
	)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}

	timeWindowObj, diags := types.ObjectValue(
		resource_gardener_shoot.TimeWindowValue{}.AttributeTypes(ctx),
		map[string]attr.Value{
			"begin": basetypes.NewStringValue(shootCluster.Maintenance.TimeWindow.Begin),
			"end":   basetypes.NewStringValue(shootCluster.Maintenance.TimeWindow.End),
		},
	)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}

	maintenanceVal, diags := resource_gardener_shoot.NewMaintenanceValue(
		resource_gardener_shoot.MaintenanceValue{}.AttributeTypes(ctx),
		map[string]attr.Value{
			"auto_update": autoUpdateObj,
			"time_window": timeWindowObj,
		},
	)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}
	data.Maintenance = maintenanceVal

	// Build workers from API response (data may be empty during import)
	var workersList []resource_gardener_shoot.WorkersValue
	for _, worker := range shootCluster.ShootProvider.Workers {
		annotations := []resource_gardener_shoot.AnnotationsValue{}
		if worker.Annotations != nil {
			for _, a := range *worker.Annotations {
				av, d := resource_gardener_shoot.NewAnnotationsValue(
					resource_gardener_shoot.AnnotationsValue{}.AttributeTypes(ctx),
					map[string]attr.Value{
						"key":   basetypes.NewStringValue(a.Key),
						"value": basetypes.NewStringValue(a.Value),
					},
				)
				diag.Append(d...)
				if diag.HasError() {
					return
				}
				annotations = append(annotations, av)
			}
		}
		annotationsListValue, diags := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.AnnotationsValue{}.Type(ctx), annotations)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}

		labels := []resource_gardener_shoot.LabelsValue{}
		if worker.Labels != nil {
			for _, l := range *worker.Labels {
				labelVal := ""
				if l.Value != nil {
					labelVal = *l.Value
				}
				lv, d := resource_gardener_shoot.NewLabelsValue(
					resource_gardener_shoot.LabelsValue{}.AttributeTypes(ctx),
					map[string]attr.Value{
						"key":   basetypes.NewStringValue(l.Key),
						"value": basetypes.NewStringValue(labelVal),
					},
				)
				diag.Append(d...)
				if diag.HasError() {
					return
				}
				labels = append(labels, lv)
			}
		}
		labelsListValue, diags := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.LabelsValue{}.Type(ctx), labels)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}

		taints := []resource_gardener_shoot.TaintsValue{}
		if worker.Taints != nil {
			for _, t := range *worker.Taints {
				tv, d := resource_gardener_shoot.NewTaintsValue(
					resource_gardener_shoot.TaintsValue{}.AttributeTypes(ctx),
					map[string]attr.Value{
						"key":    basetypes.NewStringValue(t.Key),
						"value":  basetypes.NewStringValue(t.Value),
						"effect": basetypes.NewStringValue(string(t.Effect)),
					},
				)
				diag.Append(d...)
				if diag.HasError() {
					return
				}
				taints = append(taints, tv)
			}
		}
		taintsListValue, diags := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.TaintsValue{}.Type(ctx), taints)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}

		zonesValue, diags := basetypes.NewListValueFrom(ctx, basetypes.StringType{}, worker.Zones)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}

		machineObj, diags := types.ObjectValue(
			resource_gardener_shoot.MachineValue{}.AttributeTypes(ctx),
			map[string]attr.Value{
				"image_name":    basetypes.NewStringValue(worker.Machine.Image.Name),
				"image_version": basetypes.NewStringValue(worker.Machine.Image.Version),
				"type":          basetypes.NewStringValue(worker.Machine.Type),
			},
		)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}

		maxUnavailable := int64(0)
		if worker.MaxUnavailable != nil {
			maxUnavailable = int64(*worker.MaxUnavailable)
		}

		workerVal, diags := resource_gardener_shoot.NewWorkersValue(
			resource_gardener_shoot.WorkersValue{}.AttributeTypes(ctx),
			map[string]attr.Value{
				"annotations":     annotationsListValue,
				"labels":          labelsListValue,
				"machine":         machineObj,
				"max_surge":       basetypes.NewInt64Value(int64(worker.MaxSurge)),
				"max_unavailable": basetypes.NewInt64Value(maxUnavailable),
				"maximum":         basetypes.NewInt64Value(int64(worker.Maximum)),
				"minimum":         basetypes.NewInt64Value(int64(worker.Minimum)),
				"name":            basetypes.NewStringValue(worker.Name),
				"taints":          taintsListValue,
				"volume_size":     basetypes.NewStringValue(worker.Volume.Size),
				"zones":           zonesValue,
			},
		)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}
		workersList = append(workersList, workerVal)
	}

	workersListValue, diags := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.WorkersValue{}.Type(ctx), workersList)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}

	shootProviderVal, diags := resource_gardener_shoot.NewShootProviderValue(
		resource_gardener_shoot.ShootProviderValue{}.AttributeTypes(ctx),
		map[string]attr.Value{
			"infrastructure_config":  infraConfigObj,
			"load_balancer_provider": loadBalancerProviderVal,
			"workers":                workersListValue,
		},
	)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}
	data.ShootProvider = shootProviderVal
}

const (
	shootReconcilePollInterval   = 15 * time.Second
	shootReconcileTimeout        = 60 * time.Minute
	shootReconcileRequestTimeout = 2 * time.Minute // Per-request limit; connection may be stale after sleep
	shootReconcileMaxRetries     = 5
)

// isRetriableStatus returns true for HTTP statuses that may be transient (e.g. 403 Forbidden IP after wake from sleep).
func isRetriableStatus(statusCode int) bool {
	switch statusCode {
	case 403: // Forbidden IP - can occur during network transition when screen locks/unlocks
	case 429: // Too Many Requests
	case 502, 503, 504: // Bad Gateway, Service Unavailable, Gateway Timeout
		return true
	}
	return false
}

func WaitForShootReconcile(ctx context.Context, client *cleura.Client, gardenerRegionTag, openStackRegionTag, openStackProjectId, shootName string, waitForDelete bool) diag.Diagnostics {
	var diagnostics diag.Diagnostics

	// Use wall-clock deadline so timeout is correct after machine sleep/suspend.
	// context.WithTimeout uses monotonic timers that don't advance when suspended.
	deadline := time.Now().Add(shootReconcileTimeout)

	ticker := time.NewTicker(shootReconcilePollInterval)
	defer ticker.Stop()

	for {
		// Check wall-clock deadline (survives suspend/resume)
		if time.Now().After(deadline) {
			if waitForDelete {
				diagnostics.AddError("Timeout waiting for shoot deletion",
					"shoot still exists after the timeout")
			} else {
				diagnostics.AddError("Timeout waiting for shoot reconciliation",
					"shoot did not reach Progress=100 and State=Succeeded within the timeout")
			}
			return diagnostics
		}

		// Respect Terraform context cancellation (e.g. Ctrl+C)
		select {
		case <-ctx.Done():
			diagnostics.AddError("Shoot reconciliation cancelled", ctx.Err().Error())
			return diagnostics
		default:
		}

		// Per-request timeout so a single call can't hang forever after connection drop
		reqCtx, reqCancel := context.WithTimeout(ctx, shootReconcileRequestTimeout)

		var response *api.GardenerGetShootResponse
		var err error
		for attempt := 0; attempt < shootReconcileMaxRetries; attempt++ {
			response, err = client.GardenerGetShootWithResponse(reqCtx, gardenerRegionTag, openStackRegionTag, openStackProjectId, shootName)
			if err == nil && response != nil {
				// For delete: 404 means shoot is gone, which is success
				if waitForDelete && response.StatusCode() == 404 {
					reqCancel()
					return diagnostics
				}
				// Retry on transient HTTP errors (e.g. 403 Forbidden IP when network changes during screen lock)
				if !isRetriableStatus(response.StatusCode()) {
					break
				}
			}
			// Retry on error or retriable status (e.g. connection reset, 403 after sleep)
			if attempt < shootReconcileMaxRetries-1 {
				backoff := time.Duration(attempt+1) * 5 * time.Second
				select {
				case <-ctx.Done():
					reqCancel()
					diagnostics.AddError("Shoot reconciliation cancelled", ctx.Err().Error())
					return diagnostics
				case <-time.After(backoff):
				}
			}
		}
		reqCancel()

		if err != nil {
			diagnostics.AddError("Failed to get shoot status", err.Error())
			return diagnostics
		}

		// For delete: 404 means shoot is gone, which is success (may have been set in retry loop)
		if waitForDelete && response != nil && response.StatusCode() == 404 {
			return diagnostics
		}

		if response == nil || response.JSON200 == nil {
			statusStr := "unknown"
			bodyStr := ""
			if response != nil {
				statusStr = fmt.Sprintf("%d", response.StatusCode())
				bodyStr = string(response.Body)
			}
			diagnostics.AddError("API error", fmt.Sprintf("unexpected response status %s: %s", statusStr, bodyStr))
			return diagnostics
		}

		shoot := response.JSON200
		lastOp := shoot.LastOperation

		if lastOp == nil {
			// No last operation yet (e.g. shoot just created), keep polling
		} else if lastOp.Progress == 100 && lastOp.State == api.GardenerShootLastOperationStateSucceeded {
			return diagnostics
		} else if lastOp.State == api.GardenerShootLastOperationStateError || lastOp.State == api.GardenerShootLastOperationStateFailed || lastOp.State == api.GardenerShootLastOperationStateAborted {
			diagnostics.AddError("Shoot reconciliation failed",
				fmt.Sprintf("last operation state: %s, description: %s", lastOp.State, lastOp.Description))
			return diagnostics
		}

		select {
		case <-ctx.Done():
			diagnostics.AddError("Shoot reconciliation cancelled", ctx.Err().Error())
			return diagnostics
		case <-ticker.C:
			// Poll again
		}
	}
}
