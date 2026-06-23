package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/cleura/terraform-provider-cleura/api"
	"github.com/cleura/terraform-provider-cleura/cleura"
	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

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
