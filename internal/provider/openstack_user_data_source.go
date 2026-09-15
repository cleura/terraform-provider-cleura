package provider

import (
	"context"
	"fmt"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource                     = (*openstackUserDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*openstackUserDataSource)(nil)
)

func NewOpenStackUserDataSource() datasource.DataSource {
	return &openstackUserDataSource{}
}

type openstackUserDataSource struct {
	config *ProviderConfig
}

type openstackUserDataSourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	DomainID         types.String `tfsdk:"domain_id"`
	Description      types.String `tfsdk:"description"`
	Enabled          types.Bool   `tfsdk:"enabled"`
	DefaultProjectID types.String `tfsdk:"default_project_id"`
}

func (d *openstackUserDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.config = fromDataSource(ctx, req, resp)
}

func (d *openstackUserDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_openstack_user"
}

func (d *openstackUserDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *openstackUserDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches an OpenStack (Keystone) user in your Cleura account by `id` or by `name`. " +
			"The lookup is scoped to one OpenStack domain: by default the domain that serves the provider's " +
			"`region`, or `domain_id` when set. OpenStack users are distinct from Cleura account users.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "OpenStack user ID to fetch. Exactly one of `id` and `name` must be set.",
			},
			"name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "User name to fetch, matched exactly within the domain. Exactly one of `id` " +
					"and `name` must be set.",
			},
			"domain_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack domain to search. Defaults to the account's domain that serves the " +
					"provider's `region` (or the account's only domain).",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "User description, if any.",
			},
			"enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the user can log in.",
			},
			"default_project_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user's default OpenStack project, if Cleura has assigned one.",
			},
		},
	}
}

func (d *openstackUserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !require(d.config, &resp.Diagnostics, false) {
		return
	}

	var data openstackUserDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, d.config, data.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}
	users, err := listUsers(ctx, d.config, domainID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list OpenStack users", err.Error())
		return
	}

	var user *api.OpenStackIdentityUser
	var found bool
	if !data.ID.IsNull() {
		user, found = findUserByID(users, data.ID.ValueString())
		if !found {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "OpenStack user not found",
				fmt.Sprintf("No OpenStack user with ID %q exists in domain %s.", data.ID.ValueString(), domainID))
			return
		}
	} else {
		user, found = findUserByName(users, data.Name.ValueString())
		if !found {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "OpenStack user not found",
				fmt.Sprintf("No OpenStack user named %q exists in domain %s.", data.Name.ValueString(), domainID))
			return
		}
	}

	data.ID = types.StringValue(user.Id)
	data.Name = types.StringValue(user.Name)
	data.DomainID = types.StringValue(user.DomainId)
	data.Description = optionalStringValue(user.Description)
	data.Enabled = types.BoolValue(user.Enabled)
	data.DefaultProjectID = optionalStringValue(user.DefaultProjectId)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
