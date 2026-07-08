package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/cleura/cleura-client-go/cleura"
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
	Cloud     types.String `tfsdk:"cloud"`
	Region    types.String `tfsdk:"region"`
	ProjectID types.String `tfsdk:"project_id"`
	Url       types.String `tfsdk:"url"`
	Username  types.String `tfsdk:"username"`
	Token     types.String `tfsdk:"token"`
	Profile   types.String `tfsdk:"profile"`
	UseCli    types.Bool   `tfsdk:"use_cli"`
}

// cleuraProvider is the provider implementation.
type cleuraProvider struct {
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
			"cloud": schema.StringAttribute{
				Description: "Cleura cloud: `public`, `compliant`, or the name of a private cloud (e.g. `acme-corp`). Used as the Gardener region tag. Only public and compliant have a default API URL; private clouds require url. May also be provided via CLEURA_CLOUD.",
				Optional:    true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9\-]*$`), "must be public, compliant, or a private cloud name"),
				},
			},
			"region": schema.StringAttribute{
				Description: "OpenStack region tag for this provider configuration (e.g. sto2, sto-com). May also be provided via CLEURA_REGION.",
				Optional:    true,
			},
			"project_id": schema.StringAttribute{
				Description: "OpenStack project ID for Gardener resources. Optional when only using data sources; required for cleura_gardener_shoot and cleura_gardener_shoot_kubeconfig. May also be provided via CLEURA_PROJECT_ID.",
				Optional:    true,
			},
			"url": schema.StringAttribute{
				Description: "URI for the Cleura Cloud API. Required for private clouds. When omitted, defaults for public and compliant cloud only. May also be provided via CLEURA_API_URL.",
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
			"profile": schema.StringAttribute{
				Description: "cleura CLI profile to read fallback credentials from. Only consulted when username/token are not set in the configuration or environment. Defaults to the CLI's own current profile.",
				Optional:    true,
			},
			"use_cli": schema.BoolAttribute{
				Description: "Fall back to credentials from the cleura CLI ('cleura login') when username/token are not set in the configuration or environment. Defaults to true.",
				Optional:    true,
			},
		},
	}
}

func (p *cleuraProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	tflog.Info(ctx, "Configuring Cleura client")

	var config cleuraProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	for _, check := range []struct {
		name string
		val  types.String
		env  string
	}{
		{"cloud", config.Cloud, "CLEURA_CLOUD"},
		{"region", config.Region, "CLEURA_REGION"},
		{"project_id", config.ProjectID, "CLEURA_PROJECT_ID"},
		{"url", config.Url, "CLEURA_API_URL"},
		{"username", config.Username, "CLEURA_API_USERNAME"},
		{"token", config.Token, "CLEURA_API_TOKEN"},
	} {
		if check.val.IsUnknown() {
			resp.Diagnostics.AddAttributeError(
				path.Root(check.name),
				fmtUnknownTitle(check.name),
				fmtUnknownDetail(check.name, check.env),
			)
		}
	}

	if resp.Diagnostics.HasError() {
		return
	}

	cloud := stringOrEnv(config.Cloud, "CLEURA_CLOUD")
	region := stringOrEnv(config.Region, "CLEURA_REGION")
	projectID := stringOrEnv(config.ProjectID, "CLEURA_PROJECT_ID")
	url := stringOrEnv(config.Url, "CLEURA_API_URL")
	username := stringOrEnv(config.Username, "CLEURA_API_USERNAME")
	token := stringOrEnv(config.Token, "CLEURA_API_TOKEN")

	// Last credential tier: the cleura CLI ('cleura login'), like azurerm
	// falls back to the az CLI. Explicit configuration and environment
	// variables always win; the CLI only fills what is still missing.
	// Region and project are deliberately not taken from the CLI —
	// infrastructure code should state topology explicitly.
	useCli := config.UseCli.IsNull() || config.UseCli.ValueBool()
	if useCli && (username == "" || token == "") {
		explicitCloud := cloud
		creds, err := cliCredentials(ctx, config.Profile.ValueString())
		switch {
		case err == nil:
			if username == "" {
				username = creds.Username
			}
			if token == "" {
				token = creds.Token
			}
			if cloud == "" {
				cloud = creds.Cloud
			}
			if url == "" && explicitCloud == "" {
				// Only adopt the CLI's endpoint when the cloud was not
				// explicitly chosen either; otherwise the cloud's own
				// default URL applies below.
				url = creds.Endpoint
			}
			tflog.Info(ctx, "Using credentials from the cleura CLI", map[string]any{"cli_profile": creds.Profile})
			if explicitCloud != "" && creds.Cloud != "" && explicitCloud != creds.Cloud {
				resp.Diagnostics.AddWarning(
					"Cleura CLI credentials may not match the configured cloud",
					fmt.Sprintf("The provider is configured for cloud %q but the cleura CLI credentials (profile %q) were created for cloud %q. The token may not be valid there; set username/token explicitly or log in to the matching profile.", explicitCloud, creds.Profile, creds.Cloud),
				)
			}
			if at, ok := creds.storedAt(); ok && time.Since(at) > 20*time.Hour {
				resp.Diagnostics.AddWarning(
					"Cleura CLI token may be expired",
					fmt.Sprintf("The token from the cleura CLI (profile %q) was stored %s ago and Cleura tokens are short-lived. If authentication fails, run 'cleura login'.", creds.Profile, roughAge(time.Since(at))),
				)
			}
		case errors.Is(err, errCLINoCredentials):
			// Nothing stored: fall through to the standard errors below.
		default:
			resp.Diagnostics.AddWarning("Could not read credentials from the cleura CLI", err.Error())
		}
	}

	if cloud == "" {
		resp.Diagnostics.AddAttributeError(path.Root("cloud"), "Missing Cleura cloud", "Set cloud in the provider configuration or use the CLEURA_CLOUD environment variable.")
	}
	if region == "" {
		resp.Diagnostics.AddAttributeError(path.Root("region"), "Missing Cleura region", "Set region in the provider configuration or use the CLEURA_REGION environment variable.")
	}
	if username == "" {
		resp.Diagnostics.AddAttributeError(path.Root("username"), "Missing Cleura API username", "Set username in the provider configuration or use the CLEURA_API_USERNAME environment variable, or run 'cleura login' (the provider falls back to cleura CLI credentials).")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing Cleura API token", "Set token in the provider configuration or use the CLEURA_API_TOKEN environment variable, or run 'cleura login' (the provider falls back to cleura CLI credentials).")
	}

	if url == "" {
		defaultURL, err := cleura.DefaultAPIURL(cloud)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing Cleura API url", err.Error()+" Set url in the provider configuration or use the CLEURA_API_URL environment variable.")
		} else {
			url = defaultURL
		}
	}

	if resp.Diagnostics.HasError() {
		return
	}

	ctx = tflog.SetField(ctx, "cleura_cloud", cloud)
	ctx = tflog.SetField(ctx, "cleura_region", region)
	ctx = tflog.SetField(ctx, "cleura_project_id", projectID)
	ctx = tflog.SetField(ctx, "cleura_url", url)
	ctx = tflog.SetField(ctx, "cleura_username", username)
	ctx = tflog.MaskFieldValuesWithFieldKeys(ctx, "cleura_token")

	tflog.Debug(ctx, "Creating Cleura client")

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

	providerConfig := &ProviderConfig{
		Client:    client,
		Cloud:     cloud,
		Region:    region,
		ProjectID: projectID,
	}

	resp.DataSourceData = providerConfig
	resp.ResourceData = providerConfig

	tflog.Info(ctx, "Configured Cleura client", map[string]any{"success": true})
}

func stringOrEnv(config types.String, envKey string) string {
	if !config.IsNull() {
		return config.ValueString()
	}
	return os.Getenv(envKey)
}

func fmtUnknownTitle(attr string) string {
	return "Unknown Cleura provider attribute: " + attr
}

func fmtUnknownDetail(attr, envKey string) string {
	return "The provider cannot be configured while " + attr + " is unknown. " +
		"Set a static value in the provider configuration or use the " + envKey + " environment variable."
}

// DataSources defines the data sources implemented in the provider.
func (p *cleuraProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewProjectDataSource,
	}
}

// Resources defines the resources implemented in the provider.
func (p *cleuraProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewGardenerShootResource,
		NewGardenerShootKubeconfigResource,
	}
}

func (p *cleuraProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{}
}
