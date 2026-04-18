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

var _ datasource.DataSource = (*shootCloudprofileDataSource)(nil)

func NewShootCloudprofileDataSource() datasource.DataSource {
	return &shootCloudprofileDataSource{}
}

type shootCloudprofileDataSource struct {
	client *cleura.Client
}

type shootCloudprofileDataSourceModel struct {
	GardenerRegionTag  types.String                        `tfsdk:"gardener_region_tag"`
	OpenStackRegionTag types.String                        `tfsdk:"open_stack_region_tag"`
	Kubernetes         *shootCloudprofileKubernetesModel   `tfsdk:"kubernetes"`
	MachineImage       *shootCloudprofileMachineImageModel `tfsdk:"machine_image"`
}

type shootCloudprofileKubernetesModel struct {
	MostRecent types.Bool   `tfsdk:"most_recent"`
	Version    types.String `tfsdk:"version"`
}

type shootCloudprofileMachineImageModel struct {
	MostRecent types.Bool   `tfsdk:"most_recent"`
	Version    types.String `tfsdk:"version"`
}

func (d *shootCloudprofileDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *shootCloudprofileDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_shoot_cloudprofile"
}

func (d *shootCloudprofileDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"gardener_region_tag": schema.StringAttribute{
				Required: true,
			},
			"open_stack_region_tag": schema.StringAttribute{
				Required: true,
			},
			"kubernetes": schema.SingleNestedAttribute{
				Attributes: map[string]schema.Attribute{
					"most_recent": schema.BoolAttribute{
						Required: true,
					},
					"version": schema.StringAttribute{
						Optional: true,
						Computed: true,
					},
				},
				Required: true,
			},
			"machine_image": schema.SingleNestedAttribute{
				Attributes: map[string]schema.Attribute{
					"most_recent": schema.BoolAttribute{
						Required: true,
					},
					"version": schema.StringAttribute{
						Optional: true,
						Computed: true,
					},
				},
				Required: true,
			},
		},
	}
}

func (d *shootCloudprofileDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data shootCloudprofileDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Read API call logic
	response, err := d.client.GardenerListCloudProfiles(ctx, data.GardenerRegionTag.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to list shoot cloudprofiles", err.Error())
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

	var cloudprofiles []api.GardenerCloudProfileCloudProfile
	if err := json.Unmarshal(body, &cloudprofiles); err != nil {
		resp.Diagnostics.AddError("Failed to unmarshal response", err.Error())
		return
	}
	var kubernetesVersion *string
	for _, k := range cloudprofiles {
		for _, version := range k.Kubernetes.Versions {
			if data.Kubernetes.Version.ValueString() != "" && version.Version == data.Kubernetes.Version.ValueString() {
				if *version.Classification == api.Supported {
					kubernetesVersion = &version.Version
				}
			}
		}
	}

	if kubernetesVersion == nil {
		resp.Diagnostics.AddError("Kubernetes version not found", fmt.Sprintf("Kubernetes version %s does not exist. Update you query to an existing image", data.Kubernetes.Version.ValueString()))
		return
	}

	// var region *api.IdentityRegionWithProjects
	// for _, r := range regions {
	// 	if r.Region.Tag == data.OpenStackRegionTag.ValueString() {
	// 		region = &r
	// 	}
	// }
	// // If no region was found, return an error
	// if region == nil {
	// 	resp.Diagnostics.AddError("Region not found", fmt.Sprintf("The region tag %s could not be found, update your query to match one of the available regions", data.OpenStackRegionTag.ValueString()))
	// 	return
	// }

	// var project *api.IdentityProject
	// for _, p := range region.Projects {
	// 	if p.Name == data.Name.ValueString() {
	// 		project = &p
	// 	}
	// }
	// // If no project was found, return an error
	// if project == nil {
	// 	resp.Diagnostics.AddError("Project not found", fmt.Sprintf("No project named %s was found. Make sure the name is correct and the authenticated user has access to the project", data.OpenStackRegionTag.ValueString()))
	// 	return
	// }

	// data.Id = types.StringValue(project.Id)

	// // Example data value setting
	// data.Id = types.StringValue("example-id")

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
