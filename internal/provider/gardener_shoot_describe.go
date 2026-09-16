package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

// withShootDescriptions injects the reviewed MarkdownDescription text from
// shootAttributeDescriptions into the generated cleura_gardener_shoot schema.
// The generated schema (resource_gardener_shoot, DO NOT EDIT) carries no
// descriptions, so we set them on a copy here instead of editing generated code.
func withShootDescriptions(s schema.Schema) schema.Schema {
	if d := shootAttributeDescriptions[""]; d != "" {
		s.MarkdownDescription = d
	}
	s.Attributes = describeShootAttributes(s.Attributes, "")
	s.Attributes["project_id"] = shootProjectIDAttribute()
	return s
}

// shootProjectIDAttribute is the project the shoot is created in. It is not in
// the generated schema: project_id is a path parameter, and the generator omits
// those. It defaults to the provider's project_id, and is recorded in state so
// that a later edit of the provider's value does not retarget an existing
// cluster. A shoot cannot move between projects, so a change forces replacement.
func shootProjectIDAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: "OpenStack project the cluster is created in. Defaults to the provider's " +
			"`project_id`. Set it explicitly to place the cluster in a project created in the same " +
			"configuration — the provider's `project_id` must be known before the run starts, so it cannot " +
			"refer to a `cleura_openstack_project` this provider manages. Recorded in state, so changing the " +
			"provider's `project_id` later does not move an existing cluster. Changing it forces a new cluster.",
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.UseStateForUnknown(),
			stringplanmodifier.RequiresReplace(),
		},
	}
}

// describeShootAttributes walks the attribute tree, setting MarkdownDescription
// by dotted path and recursing into nested attributes. Attribute values are
// copied and the map entry reassigned, so the generated schema is not mutated.
func describeShootAttributes(attrs map[string]schema.Attribute, prefix string) map[string]schema.Attribute {
	out := make(map[string]schema.Attribute, len(attrs))
	for name, attr := range attrs {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		d := shootAttributeDescriptions[path]
		switch a := attr.(type) {
		case schema.StringAttribute:
			if d != "" {
				a.MarkdownDescription = d
			}
			out[name] = a
		case schema.BoolAttribute:
			if d != "" {
				a.MarkdownDescription = d
			}
			out[name] = a
		case schema.Int64Attribute:
			if d != "" {
				a.MarkdownDescription = d
			}
			out[name] = a
		case schema.ListAttribute:
			if d != "" {
				a.MarkdownDescription = d
			}
			out[name] = a
		case schema.SingleNestedAttribute:
			if d != "" {
				a.MarkdownDescription = d
			}
			a.Attributes = describeShootAttributes(a.Attributes, path)
			out[name] = a
		case schema.ListNestedAttribute:
			if d != "" {
				a.MarkdownDescription = d
			}
			a.NestedObject.Attributes = describeShootAttributes(a.NestedObject.Attributes, path)
			out[name] = a
		default:
			out[name] = attr
		}
	}
	return out
}
