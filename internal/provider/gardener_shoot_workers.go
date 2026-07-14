package provider

import (
	"context"
	"encoding/json"

	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	api "github.com/cleura/cleura-client-go/api"
)

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
