package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cleura/terraform-provider-cleura/internal/provider/resource_gardener_shoot"
)

// shootModel is the generated shoot model plus project_id.
//
// The generated model (resource_gardener_shoot, DO NOT EDIT) carries only the
// attributes derived from the API body; project_id is a path parameter, which
// the generator leaves out. Embedding by value — which terraform-plugin-framework
// maps into the schema's object type — adds the attribute without editing
// generated code, so ./generate.sh stays safe to re-run.
//
// Pass &data.GardenerShootModel to helpers that take the generated type.
type shootModel struct {
	resource_gardener_shoot.GardenerShootModel

	ProjectID types.String `tfsdk:"project_id"`
}
