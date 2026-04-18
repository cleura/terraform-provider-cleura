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
	client *cleura.Client
}

type projectDataSourceModel struct {
	Id                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	OpenStackRegionTag types.String `tfsdk:"open_stack_region_tag"`
}

func (d *projectDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*cleura.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *cleura.Client, got %T", req.ProviderData),
		)
		return
	}

	d.client = client
}

func (d *projectDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			"name": schema.StringAttribute{
				Required: true,
			},
			"open_stack_region_tag": schema.StringAttribute{
				Required: true,
			},
		},
	}
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data projectDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Read API call logic
	response, err := d.client.IdentityListRegionsWithProjects(ctx)
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
		if r.Region.Tag == data.OpenStackRegionTag.ValueString() {
			region = &r
		}
	}
	// If no region was found, return an error
	if region == nil {
		resp.Diagnostics.AddError("Region not found", fmt.Sprintf("The region tag %s could not be found, update your query to match one of the available regions", data.OpenStackRegionTag.ValueString()))
		return
	}

	var project *api.IdentityProject
	for _, p := range region.Projects {
		if p.Name == data.Name.ValueString() {
			project = &p
		}
	}
	// If no project was found, return an error
	if project == nil {
		resp.Diagnostics.AddError("Project not found", fmt.Sprintf("No project named %s was found. Make sure the name is correct and the authenticated user has access to the project", data.OpenStackRegionTag.ValueString()))
		return
	}

	data.Id = types.StringValue(project.Id)

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
