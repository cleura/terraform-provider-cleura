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

	// Surface a failed/aborted reconcile so a refresh/plan reflects the cluster's
	// real health instead of silently reporting "no changes". The API returns the
	// submitted spec even when the underlying operation failed (e.g. nodes could
	// not be provisioned), so lastOperation is the only signal of trouble. This is
	// a warning, not an error, so the user can still plan a fix.
	if op := shootCluster.LastOperation; op != nil {
		switch op.State {
		case api.GardenerShootLastOperationStateError,
			api.GardenerShootLastOperationStateFailed,
			api.GardenerShootLastOperationStateAborted:
			diag.AddWarning(
				"Cluster reconciliation has not succeeded",
				fmt.Sprintf("Shoot %q: the last %s operation is %q (%d%% complete).\n\n"+
					"The running cluster may not match its configuration until this is resolved. "+
					"Once the cause (shown below) is addressed, trigger a fresh reconcile — either run "+
					"one from the Cleura Cloud Control Panel, or apply a configuration change through Terraform.\n\n"+
					"API message:\n%s",
					data.Name.ValueString(), op.Type, op.State, op.Progress, op.Description),
			)
		}
	}

	// AllowedCidrs
	if shootCluster.AllowedCidrs != nil && len(*shootCluster.AllowedCidrs) > 0 {
		// allowed_cidrs is a set; preserve the user's configured order in case the
		// API returns it in a different order.
		cidrs := preserveOrder(ctx, *shootCluster.AllowedCidrs, data.AllowedCidrs, func(c string) string { return c })
		allowedCidrsVal, diags := basetypes.NewListValueFrom(ctx, basetypes.StringType{}, cidrs)
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
			start := ""
			if schedule.Start != nil {
				start = *schedule.Start
			}
			end := ""
			if schedule.End != nil {
				end = *schedule.End
			}
			// Use the generated constructor so the element's state is Known. A bare
			// struct literal leaves state at its zero value (Null), which serializes
			// to a null object and drops start/end.
			hv, d := resource_gardener_shoot.NewHibernationSchedulesValue(
				resource_gardener_shoot.HibernationSchedulesValue{}.AttributeTypes(ctx),
				map[string]attr.Value{
					"start": basetypes.NewStringValue(start),
					"end":   basetypes.NewStringValue(end),
				},
			)
			diag.Append(d...)
			if diag.HasError() {
				return
			}
			hibernationSchedules = append(hibernationSchedules, hv)
		}
		// Preserve the user's configured schedule order, keyed by start+end, in case
		// the API returns schedules in a different order. (\x00 separator avoids any
		// collision since cron strings never contain a null byte.)
		hibernationSchedules = preserveOrder(ctx, hibernationSchedules, data.HibernationSchedules, func(h resource_gardener_shoot.HibernationSchedulesValue) string {
			return h.Start.ValueString() + "\x00" + h.End.ValueString()
		})
		data.HibernationSchedules, diags = basetypes.NewListValueFrom(ctx, resource_gardener_shoot.HibernationSchedulesValue{}.Type(ctx), hibernationSchedules)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}
	} else if data.HibernationSchedules.IsNull() || data.HibernationSchedules.IsUnknown() {
		// Only set to null if it wasn't already set by the plan/config.
		// This prevents dropping the user's config if the API omits the field.
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

	// Networking is always present in the read response. cilium_provider_config is
	// nil when the networking type is not cilium -> map it to a null object.
	var ciliumObj basetypes.ObjectValue
	if shootCluster.Networking.CiliumProviderConfig != nil {
		c := shootCluster.Networking.CiliumProviderConfig
		encMode := basetypes.NewStringNull()
		if c.EncryptionMode != nil {
			encMode = basetypes.NewStringValue(string(*c.EncryptionMode))
		}
		nodeToNode := basetypes.NewBoolNull()
		if c.EncryptionNodeToNodeEnabled != nil {
			nodeToNode = basetypes.NewBoolValue(*c.EncryptionNodeToNodeEnabled)
		}
		strictMode := basetypes.NewBoolNull()
		if c.EncryptionStrictModeEnabled != nil {
			strictMode = basetypes.NewBoolValue(*c.EncryptionStrictModeEnabled)
		}
		ciliumObj, diags = types.ObjectValue(
			resource_gardener_shoot.CiliumProviderConfigValue{}.AttributeTypes(ctx),
			map[string]attr.Value{
				"debug":                           basetypes.NewBoolValue(c.Debug),
				"encryption_enabled":              basetypes.NewBoolValue(c.EncryptionEnabled),
				"encryption_mode":                 encMode,
				"encryption_node_to_node_enabled": nodeToNode,
				"encryption_strict_mode_enabled":  strictMode,
				"hubble_enabled":                  basetypes.NewBoolValue(c.HubbleEnabled),
				"policy_audit_mode":               basetypes.NewBoolValue(c.PolicyAuditMode),
				"tunnel":                          basetypes.NewStringValue(string(c.Tunnel)),
			},
		)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}
	} else {
		ciliumObj = basetypes.NewObjectNull(resource_gardener_shoot.CiliumProviderConfigValue{}.AttributeTypes(ctx))
	}

	networkingVal, diags := resource_gardener_shoot.NewNetworkingValue(
		resource_gardener_shoot.NetworkingValue{}.AttributeTypes(ctx),
		map[string]attr.Value{
			"cilium_provider_config": ciliumObj,
			"nodes":                  basetypes.NewStringValue(shootCluster.Networking.Nodes),
			"type":                   basetypes.NewStringValue(string(shootCluster.Networking.Type)),
		},
	)
	diag.Append(diags...)
	if diag.HasError() {
		return
	}
	data.Networking = networkingVal

	// Build workers from API response (data may be empty during import).
	//
	// The API returns each worker's label/annotation/taint maps in a normalized
	// order that may differ from the user's configuration. Map the plan/prior
	// state workers by name so state preserves the configured order and avoids
	// "Provider produced inconsistent result after apply".
	dataWorkersByName := map[string]resource_gardener_shoot.WorkersValue{}
	if !data.ShootProvider.Workers.IsNull() && !data.ShootProvider.Workers.IsUnknown() {
		var dws []resource_gardener_shoot.WorkersValue
		if !data.ShootProvider.Workers.ElementsAs(ctx, &dws, false).HasError() {
			for _, dw := range dws {
				dataWorkersByName[dw.Name.ValueString()] = dw
			}
		}
	}

	var workersList []resource_gardener_shoot.WorkersValue
	for _, worker := range shootCluster.ShootProvider.Workers {
		dataWorker := dataWorkersByName[worker.Name]

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
		annotations = preserveOrder(ctx, annotations, dataWorker.Annotations, func(a resource_gardener_shoot.AnnotationsValue) string { return a.Key.ValueString() })
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
		labels = preserveOrder(ctx, labels, dataWorker.Labels, func(l resource_gardener_shoot.LabelsValue) string { return l.Key.ValueString() })
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
		// Taints are identified by (key, effect) — the same key with different
		// effects is valid — so key the reorder by both, else preserveOrder's dedup
		// would silently drop a distinct taint.
		taints = preserveOrder(ctx, taints, dataWorker.Taints, func(t resource_gardener_shoot.TaintsValue) string {
			return t.Key.ValueString() + "\x00" + t.Effect.ValueString()
		})
		taintsListValue, diags := basetypes.NewListValueFrom(ctx, resource_gardener_shoot.TaintsValue{}.Type(ctx), taints)
		diag.Append(diags...)
		if diag.HasError() {
			return
		}

		// Preserve the user's configured zone order in case the API reorders them.
		zones := preserveOrder(ctx, worker.Zones, dataWorker.Zones, func(z string) string { return z })
		zonesValue, diags := basetypes.NewListValueFrom(ctx, basetypes.StringType{}, zones)
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

	// Preserve the user's configured worker order; the API may return workers in a
	// different order, which would otherwise cause inconsistent-result errors.
	workersList = preserveOrder(ctx, workersList, data.ShootProvider.Workers, func(w resource_gardener_shoot.WorkersValue) string { return w.Name.ValueString() })
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

// preserveOrder reorders vals so their keys follow the order in dataList (the
// user's configured / prior-state list), keeping any entries not present in
// dataList at the end in their original order.
//
// WORKAROUND. In Kubernetes, labels and annotations are maps and taints are a set
// keyed by (key, effect); the Cleura API instead models them as ordered arrays of
// {key, value} and returns them in a normalized order. Terraform maps that array
// to an ordered list, so when the API's order differs from the user's config the
// plugin framework raises "Provider produced inconsistent result after apply".
// This helper re-sorts API responses back into the user's order to avoid that.
//
// The goal is to fix this upstream: if the API modeled labels/annotations as maps
// (and taints as a set), the generated schema would be order-independent and this
// helper — together with all its call sites — could be removed.
func preserveOrder[T any](ctx context.Context, vals []T, dataList basetypes.ListValue, keyOf func(T) string) []T {
	if len(vals) <= 1 || dataList.IsNull() || dataList.IsUnknown() {
		return vals
	}
	var dataVals []T
	if dataList.ElementsAs(ctx, &dataVals, false).HasError() {
		return vals
	}
	byKey := make(map[string]T, len(vals))
	for _, v := range vals {
		byKey[keyOf(v)] = v
	}
	out := make([]T, 0, len(vals))
	seen := make(map[string]struct{}, len(vals))
	for _, dv := range dataVals {
		k := keyOf(dv)
		if v, ok := byKey[k]; ok {
			if _, dup := seen[k]; !dup {
				out = append(out, v)
				seen[k] = struct{}{}
			}
		}
	}
	for _, v := range vals {
		k := keyOf(v)
		if _, ok := seen[k]; !ok {
			out = append(out, v)
			seen[k] = struct{}{}
		}
	}
	return out
}
