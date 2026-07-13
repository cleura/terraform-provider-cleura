package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
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
		Description: "Interact with Cleura Cloud. Authenticate by running `cleura login` — the provider uses the CLI's credentials automatically; set the CLEURA_API_USERNAME / CLEURA_API_TOKEN environment variables or the username / token attributes to override.",
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
	// The CLI-tier attributes must be known too: an unknown profile would
	// silently query the CLI's current profile and an unknown use_cli would
	// silently disable the tier — plan and apply would then use different
	// credential sources.
	if config.Profile.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("profile"), fmtUnknownTitle("profile"),
			"The provider cannot be configured while profile is unknown. Set a static value in the provider configuration.")
	}
	if config.UseCli.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("use_cli"), fmtUnknownTitle("use_cli"),
			"The provider cannot be configured while use_cli is unknown. Set a static value in the provider configuration.")
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
	// variables always win; the CLI only fills what is still missing — and
	// only credentials. Region and project_id are deliberately never read
	// from the CLI profile: topology must not depend on operator state
	// (the lesson azurerm 4.0 learned from inherited subscriptions).
	useCli := config.UseCli.IsNull() || config.UseCli.ValueBool()
	var creds *cliCredentialsEnvelope
	tokenFromCLI := false
	cliMissing := false
	cliNote := ""
	if useCli && (username == "" || token == "") {
		explicitCloud, explicitUsername := cloud, username
		var err error
		creds, err = cliCredentials(ctx, config.Profile.ValueString())
		switch {
		case err == nil:
			// The API authenticates username+token as a pair; assembling it
			// across tiers is a coherence hazard worth flagging.
			if explicitUsername != "" && token == "" && creds.Username != explicitUsername {
				resp.Diagnostics.AddWarning(
					"Mixed credential sources",
					fmt.Sprintf("username %q comes from the provider configuration or environment, but the token comes from cleura CLI profile %q, which belongs to %q. The pair may not authenticate; set both explicitly, or neither.", explicitUsername, creds.Profile, creds.Username),
				)
			}
			if username == "" {
				username = creds.Username
			}
			if token == "" {
				token = creds.Token
				tokenFromCLI = true
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
		case errors.Is(err, errCLITooOld):
			resp.Diagnostics.AddWarning("The cleura CLI is too old for provider integration", err.Error())
		case errors.Is(err, errCLINotFound):
			// A fallback that is not installed is a non-event for CI runs;
			// leave a trace for TF_LOG=DEBUG and adjust the guidance below.
			cliMissing = true
			tflog.Debug(ctx, "cleura CLI not found in PATH; skipping the CLI credential tier")
		case errors.Is(err, errCLINoCredentials):
			// Nothing stored: fall through to the standard errors below,
			// carrying the CLI's own reason (it names the affected profile).
			cliNote = strings.TrimPrefix(strings.TrimPrefix(err.Error(), errCLINoCredentials.Error()), ": ")
			// Our own hint already says how to log in; keep only the fact.
			cliNote = strings.TrimSuffix(cliNote, "; run 'cleura login'")
			tflog.Debug(ctx, "cleura CLI has no stored credentials", map[string]any{"reason": cliNote})
		default:
			resp.Diagnostics.AddWarning("Could not read credentials from the cleura CLI", err.Error())
		}
	}

	// The credential-error guidance must match reality: mention the CLI
	// fallback only when it is enabled, say "install" when the binary is
	// missing, and carry the CLI's own reason when it reported one.
	cliClause := ""
	switch {
	case !useCli:
		// The user disabled the tier; do not advertise it.
	case cliMissing:
		cliClause = ", or install the cleura CLI and run 'cleura login' (the provider falls back to cleura CLI credentials)"
	default:
		loginHint := "run 'cleura login'"
		if p := config.Profile.ValueString(); p != "" {
			loginHint = fmt.Sprintf("run 'cleura login --profile %s'", p)
		}
		if cliNote != "" {
			loginHint += " (cleura CLI: " + cliNote + ")"
		}
		cliClause = ", or " + loginHint + " (the provider falls back to cleura CLI credentials)"
	}

	if cloud == "" {
		resp.Diagnostics.AddAttributeError(path.Root("cloud"), "Missing Cleura cloud", "Set cloud in the provider configuration or use the CLEURA_CLOUD environment variable.")
	}
	if region == "" {
		resp.Diagnostics.AddAttributeError(path.Root("region"), "Missing Cleura region", "Set region in the provider configuration or use the CLEURA_REGION environment variable. Unlike credentials, region is deliberately not read from the cleura CLI profile: where infrastructure lives should be stated in the configuration.")
	}
	if username == "" {
		resp.Diagnostics.AddAttributeError(path.Root("username"), "Missing Cleura API username", "Set username in the provider configuration or use the CLEURA_API_USERNAME environment variable"+cliClause+".")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing Cleura API token", "Set token in the provider configuration or use the CLEURA_API_TOKEN environment variable"+cliClause+".")
	}

	if url == "" && cloud != "" { // with cloud missing, a url error would only repeat it
		defaultURL, err := cleura.DefaultAPIURL(cloud)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("url"), "Missing Cleura API url", err.Error()+". Set url in the provider configuration or use the CLEURA_API_URL environment variable.")
		} else {
			url = defaultURL
		}
	}

	// A CLI token sent to a different endpoint than it was created against
	// fails opaquely; make the divergence visible even when the cloud names
	// happen to match (e.g. an alternate deployment of the same cloud).
	if tokenFromCLI && creds.Endpoint != "" && url != "" && url != creds.Endpoint {
		resp.Diagnostics.AddWarning(
			"Cleura CLI credentials may not match the API endpoint",
			fmt.Sprintf("The token from cleura CLI profile %q was created against %s, but the provider targets %s. The token may not be valid there.", creds.Profile, creds.Endpoint, url),
		)
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

// stringOrEnv resolves an attribute with its environment fallback. An
// explicitly empty string falls through to the environment: patterns like
// `token = var.cleura_token` with a default of "" must not shadow
// CLEURA_API_TOKEN (that would invert the documented precedence).
func stringOrEnv(config types.String, envKey string) string {
	if v := config.ValueString(); !config.IsNull() && v != "" {
		return v
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
