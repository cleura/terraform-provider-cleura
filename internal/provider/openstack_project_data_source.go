package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource                     = (*openstackProjectDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*openstackProjectDataSource)(nil)
)

func NewOpenStackProjectDataSource() datasource.DataSource {
	return &openstackProjectDataSource{}
}

type openstackProjectDataSource struct {
	config *ProviderConfig
}

type openstackProjectDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	DomainID    types.String `tfsdk:"domain_id"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
}

func (d *openstackProjectDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.config = fromDataSource(ctx, req, resp)
}

func (d *openstackProjectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_openstack_project"
}

func (d *openstackProjectDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *openstackProjectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches an OpenStack (Keystone) project in your Cleura account by `id` or by `name`, " +
			"including its description and enabled state. A lookup by name is scoped to one OpenStack domain: " +
			"by default the domain that serves the provider's `region`, or `domain_id` when set.\n\n" +
			"Only projects the provider's API user can access are visible.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "OpenStack project ID to fetch. Exactly one of `id` and `name` must be set.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Project name to fetch, matched exactly (case-sensitive) within the domain. " +
					"Exactly one of `id` and `name` must be set.",
			},
			"domain_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack domain to search when looking up by `name`. Defaults to the account's " +
					"domain that serves the provider's `region` (or the account's only domain). Always returns the " +
					"domain the project belongs to.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Project description, if any.",
			},
			"enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the project is enabled.",
			},
		},
	}
}

func (d *openstackProjectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !require(d.config, &resp.Diagnostics, false) {
		return
	}

	var data openstackProjectDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projects, err := listProjects(ctx, d.config)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list OpenStack projects", err.Error())
		return
	}

	if !data.ID.IsNull() {
		project, found := findProjectByID(projects, data.ID.ValueString())
		if !found {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "OpenStack project not found",
				fmt.Sprintf("No OpenStack project with ID %q is visible to the authenticated user.", data.ID.ValueString()))
			return
		}
		if !data.DomainID.IsNull() && data.DomainID.ValueString() != project.DomainId {
			resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "OpenStack project is in another domain",
				fmt.Sprintf("Project %q belongs to domain %s, not %s.", project.Id, project.DomainId, data.DomainID.ValueString()))
			return
		}
		data.ID = types.StringValue(project.Id)
		data.Name = types.StringValue(project.Name)
		data.DomainID = types.StringValue(project.DomainId)
		data.Description = optionalStringValue(project.Description)
		data.Enabled = types.BoolValue(project.Enabled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	domainID, err := resolveDomainID(ctx, d.config, data.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}
	project, err := findProjectByName(projects, domainID, data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "OpenStack project not found", err.Error())
		return
	}
	data.ID = types.StringValue(project.Id)
	data.Name = types.StringValue(project.Name)
	data.DomainID = types.StringValue(project.DomainId)
	data.Description = optionalStringValue(project.Description)
	data.Enabled = types.BoolValue(project.Enabled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
