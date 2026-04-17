package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	cleura "github.com/cleura/terraform-provider-cleura/client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ provider.Provider              = &cleuraProvider{}
	_ provider.ProviderWithFunctions = &cleuraProvider{}
)

// New is a helper function to simplify provider server and testing implementation.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &cleuraProvider{
			version: version,
		}
	}
}

// cleuraProviderModel maps provider schema data to a Go type.
type cleuraProviderModel struct {
	Url      types.String `tfsdk:"url"`
	Username types.String `tfsdk:"username"`
	Token    types.String `tfsdk:"token"`
}

// cleuraProvider is the provider implementation.
type cleuraProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

// Metadata returns the provider type name.
func (p *cleuraProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cleura"
	resp.Version = p.version
}

// Schema defines the provider-level schema for configuration data.
func (p *cleuraProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Interact with Cleura Cloud.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Description: "URI for Cleura Cloud API. May also be provided via CLEURA_API_URL environment variable.",
				Optional:    true,
			},
			"username": schema.StringAttribute{
				Description: "Username for Cleura Cloud API. May also be provided via CLEURA_API_USERNAME environment variable.",
				Optional:    true,
			},
			"token": schema.StringAttribute{
				Description: "Token for Cleura Cloud API. May also be provided via CLEURA_API_TOKEN environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
		},
	}
}

func (p *cleuraProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring Cleura client")

	// Retrieve provider data from configuration
	var config cleuraProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// If practitioner provided a configuration value for any of the
	// attributes, it must be a known value.

	if config.Url.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("url"),
			"Unknown Cleura API URL",
			"The provider cannot create the Cleura API client as there is an unknown configuration value for the Cleura API URL. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the CLEURA_API_URL environment variable.",
		)
	}

	if config.Username.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("username"),
			"Unknown Cleura API Username",
			"The provider cannot create the Cleura API client as there is an unknown configuration value for the Cleura API username. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the CLEURA_API_USERNAME environment variable.",
		)
	}

	if config.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Unknown Cleura API token",
			"The provider cannot create the Cleura API client as there is an unknown configuration value for the Cleura API token. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the CLEURA_API_TOKEN environment variable.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Default values to environment variables, but override
	// with Terraform configuration value if set.

	url := os.Getenv("CLEURA_API_URL")
	username := os.Getenv("CLEURA_API_USERNAME")
	token := os.Getenv("CLEURA_API_TOKEN")

	if !config.Url.IsNull() {
		url = config.Url.ValueString()
	}

	if !config.Username.IsNull() {
		username = config.Username.ValueString()
	}

	if !config.Token.IsNull() {
		token = config.Token.ValueString()
	}

	// If any of the expected configurations are missing, return
	// errors with provider-specific guidance.

	if url == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("url"),
			"Missing Cleura API url",
			"The provider cannot create the Cleura API client as there is a missing or empty value for the Cleura API url. "+
				"Set the url value in the configuration or use the CLEURA_API_URL environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if username == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("username"),
			"Missing Cleura API Username",
			"The provider cannot create the Cleura API client as there is a missing or empty value for the Cleura API username. "+
				"Set the username value in the configuration or use the CLEURA_API_USERNAME environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if token == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("token"),
			"Missing Cleura API token",
			"The provider cannot create the Cleura API client as there is a missing or empty value for the Cleura API token. "+
				"Set the token value in the configuration or use the CLEURA_API_TOKEN environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	ctx = tflog.SetField(ctx, "cleura_url", url)
	ctx = tflog.SetField(ctx, "cleura_username", username)
	ctx = tflog.SetField(ctx, "cleura_token", token)
	ctx = tflog.MaskFieldValuesWithFieldKeys(ctx, "cleura_token")

	tflog.Debug(ctx, "Creating Cleura client")

	// Create a new Cleura client using the configuration values
	client, err := cleura.NewClientWithCredentials(url, username, token)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Create Cleura API Client",
			"An unexpected error occurred when creating the Cleura API client. "+
				"If the error is not clear, please contact the provider developers.\n\n"+
				"Cleura Client Error: "+err.Error(),
		)
		return
	}

	// Make the Cleura client available during DataSource and Resource
	// type Configure methods.
	resp.DataSourceData = client
	resp.ResourceData = client

	tflog.Info(ctx, "Configured Cleura client", map[string]any{"success": true})
}

// DataSources defines the data sources implemented in the provider.
func (p *cleuraProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

// Resources defines the resources implemented in the provider.
func (p *cleuraProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewShootResource,
		NewShootKubeconfigResource,
	}
}

func (p *cleuraProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{}
}
