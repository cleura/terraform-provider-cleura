package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
)

// shootModel is the generated shoot model plus id and project_id.
//
// The generated model (resource_gardener_shoot, DO NOT EDIT) carries only the
// attributes derived from the API body: the API body has no id, and project_id
// is a path parameter, which the generator leaves out. Embedding by value —
// which terraform-plugin-framework maps into the schema's object type — adds
// them without editing generated code, so ./generate.sh stays safe to re-run.
//
// Pass &data.GardenerShootModel to helpers that take the generated type.
type shootModel struct {
	resource_gardener_shoot.GardenerShootModel

	ID        types.String `tfsdk:"id"`
	ProjectID types.String `tfsdk:"project_id"`
}

// shootID is the id of a shoot, and of a kubeconfig for it: "<project_id>/<name>".
// The API looks a shoot up by project and name only (there is no lookup by
// uid), so this is the value the cluster can be found again with.
func shootID(projectID, name string) string {
	return projectID + "/" + name
}

// idAttribute is the read-only id both Gardener resources add to their schema.
// It keeps its prior value across plans: it only changes when the resource is
// replaced, and Terraform plans a replacement's create half without prior
// state, so it is unknown exactly then.
func idAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Computed:            true,
		MarkdownDescription: description,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}
