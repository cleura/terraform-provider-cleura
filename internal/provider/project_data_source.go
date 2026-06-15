package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cleura/terraform-provider-cleura/api"
	cleura "github.com/cleura/terraform-provider-cleura/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = (*projectDataSource)(nil)

func NewProjectDataSource() datasource.DataSource {
	return &projectDataSource{}
}

type projectDataSource struct {
	config *cleura.ProviderConfig
}

type projectDataSourceModel struct {
	Id   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

func (d *projectDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.config = providerConfigFromDataSource(ctx, req, resp)
}

func (d *projectDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "OpenStack project ID.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "OpenStack project name to look up in the provider region.",
			},
		},
	}
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !requireProviderConfig(d.config, &resp.Diagnostics, false) {
		return
	}

	var data projectDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	response, err := d.config.Client.IdentityListRegionsWithProjects(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list regions and projects", err.Error())
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

	var regions []api.IdentityRegionWithProjects
	if err := json.Unmarshal(body, &regions); err != nil {
		resp.Diagnostics.AddError("Failed to unmarshal response", err.Error())
		return
	}

	var region *api.IdentityRegionWithProjects
	for _, r := range regions {
		if r.Region.Tag == d.config.Region {
			region = &r
		}
	}
	if region == nil {
		resp.Diagnostics.AddError(
			"Region not found",
			fmt.Sprintf("The region %q from the provider configuration could not be found.", d.config.Region),
		)
		return
	}

	var project *api.IdentityProject
	for _, p := range region.Projects {
		if p.Name == data.Name.ValueString() {
			project = &p
		}
	}
	if project == nil {
		resp.Diagnostics.AddError(
			"Project not found",
			fmt.Sprintf("No project named %q was found in region %q.", data.Name.ValueString(), d.config.Region),
		)
		return
	}

	data.Id = types.StringValue(project.Id)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
