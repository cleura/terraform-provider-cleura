package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = (*shootOIDCKubeconfigDataSource)(nil)

func NewGardenerShootOIDCKubeconfigDataSource() datasource.DataSource {
	return &shootOIDCKubeconfigDataSource{}
}

type shootOIDCKubeconfigDataSource struct {
	config *ProviderConfig
}

type shootOIDCKubeconfigModel struct {
	ID                              types.String `tfsdk:"id"`
	ProjectID                       types.String `tfsdk:"project_id"`
	ShootName                       types.String `tfsdk:"shoot_name"`
	IssuerAlias                     types.String `tfsdk:"issuer_alias"`
	Audience                        types.String `tfsdk:"audience"`
	AuthenticationConfigurationName types.String `tfsdk:"authentication_configuration_name"`
	Kubeconfig                      types.String `tfsdk:"kubeconfig"`
}

func (d *shootOIDCKubeconfigDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.config = fromDataSource(ctx, req, resp)
}

func (d *shootOIDCKubeconfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_shoot_oidc_kubeconfig"
}

func (d *shootOIDCKubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches a kubeconfig that signs in to a Gardener cluster through one of the OIDC issuers " +
			"of its authentication configuration, using one audience as the OIDC client ID. The cluster must have " +
			"`authentication_configuration_name` set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<project_id>/<shoot_name>/<issuer_alias>/<audience>`.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "OpenStack project of the cluster. Defaults to the provider's `project_id`.",
			},
			"shoot_name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the cluster.",
			},
			"issuer_alias": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alias of the issuer to sign in through: a key of the configuration's `issuers`.",
			},
			"audience": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "One of the issuer's `audiences`, used as the OIDC client ID.",
			},
			"authentication_configuration_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The authentication configuration the cluster uses.",
			},
			"kubeconfig": schema.StringAttribute{
				Computed:            true,
				Sensitive:           true,
				MarkdownDescription: "The kubeconfig, as YAML.",
			},
		},
	}
}

func (d *shootOIDCKubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !require(d.config, &resp.Diagnostics, false) {
		return
	}
	var m shootOIDCKubeconfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	projectID, err := resolveProjectID(d.config, m.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing Cleura project_id", capitalizeFirst(err.Error())+".")
		return
	}
	shootName, alias, audience := m.ShootName.ValueString(), m.IssuerAlias.ValueString(), m.Audience.ValueString()

	shoot, found, err := fetchShoot(ctx, d.config, projectID, shootName)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the cluster", err.Error())
		return
	}
	if !found {
		resp.Diagnostics.AddAttributeError(path.Root("shoot_name"), "Cluster not found",
			fmt.Sprintf("There is no cluster %q in project %s.", shootName, projectID))
		return
	}
	configName := derefOrEmpty(shoot.Kubernetes.AuthenticationConfigurationName)
	if configName == "" {
		resp.Diagnostics.AddAttributeError(path.Root("shoot_name"), "No authentication configuration",
			fmt.Sprintf("Cluster %q has no authentication configuration. Set authentication_configuration_name on it first.", shootName))
		return
	}

	key, problem, err := d.audienceKey(ctx, configName, alias, audience)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the authentication configuration's issuers", err.Error())
		return
	}
	if problem != "" {
		resp.Diagnostics.AddError("Issuer or audience not found", problem)
		return
	}

	response, err := d.config.Client.GardenerIdentityProviderGetShootOidcKubeConfig(ctx, d.config.Cloud, d.config.Region, projectID, shootName, alias, key)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fetch the OIDC kubeconfig", err.Error())
		return
	}
	var kc api.GardenerIdentityProviderOidcKubeconfig
	if err := decodeJSON(response, &kc); err != nil {
		detail := err.Error()
		if response.StatusCode == http.StatusConflict {
			detail += "\n\nThe cluster may still be applying its authentication configuration; try again once its update has finished."
		}
		resp.Diagnostics.AddError("Failed to fetch the OIDC kubeconfig", detail)
		return
	}

	m.ID = types.StringValue(strings.Join([]string{projectID, shootName, alias, audience}, "/"))
	m.ProjectID = types.StringValue(projectID)
	m.AuthenticationConfigurationName = types.StringValue(configName)
	m.Kubeconfig = types.StringValue(kc.Kubeconfig)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// audienceKey finds the key the kubeconfig endpoint addresses an audience by.
// It returns a user-facing problem (listing what exists) when the issuer or the
// audience is not part of the configuration.
func (d *shootOIDCKubeconfigDataSource) audienceKey(ctx context.Context, configName, alias, audience string) (key, problem string, err error) {
	response, err := d.config.Client.GardenerIdentityProviderListIssuersForAuthenticationConfiguration(ctx, d.config.Cloud, configName)
	if err != nil {
		return "", "", err
	}
	var issuers []api.GardenerIdentityProviderIssuerReference
	if err := decodeJSON(response, &issuers); err != nil {
		return "", "", err
	}
	aliases := make([]string, 0, len(issuers))
	for _, iss := range issuers {
		aliases = append(aliases, iss.Alias)
		if iss.Alias != alias {
			continue
		}
		names := make([]string, 0, len(iss.Audiences))
		for _, a := range iss.Audiences {
			if a.Name == audience {
				return a.Key, "", nil
			}
			names = append(names, a.Name)
		}
		sort.Strings(names)
		return "", fmt.Sprintf("Issuer %q of authentication configuration %q has no audience %q. Its audiences are: %s.",
			alias, configName, audience, strings.Join(names, ", ")), nil
	}
	sort.Strings(aliases)
	return "", fmt.Sprintf("Authentication configuration %q has no issuer %q. Its issuers are: %s.",
		configName, alias, strings.Join(aliases, ", ")), nil
}
