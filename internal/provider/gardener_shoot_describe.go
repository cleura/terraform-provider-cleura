package provider

import "github.com/hashicorp/terraform-plugin-framework/resource/schema"

// withShootDescriptions injects the reviewed MarkdownDescription text from
// shootAttributeDescriptions into the generated cleura_gardener_shoot schema.
// The generated schema (resource_gardener_shoot, DO NOT EDIT) carries no
// descriptions, so we set them on a copy here instead of editing generated code.
func withShootDescriptions(s schema.Schema) schema.Schema {
	if d := shootAttributeDescriptions[""]; d != "" {
		s.MarkdownDescription = d
	}
	s.Attributes = describeShootAttributes(s.Attributes, "")
	return s
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
