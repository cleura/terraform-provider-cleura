package provider

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = (*authenticationConfigurationResource)(nil)
	_ resource.ResourceWithImportState    = (*authenticationConfigurationResource)(nil)
	_ resource.ResourceWithValidateConfig = (*authenticationConfigurationResource)(nil)
)

func NewGardenerAuthenticationConfigurationResource() resource.Resource {
	return &authenticationConfigurationResource{}
}

type authenticationConfigurationResource struct {
	config *ProviderConfig
}

type authConfigModel struct {
	ID      types.String           `tfsdk:"id"`
	Name    types.String           `tfsdk:"name"`
	Issuers map[string]issuerModel `tfsdk:"issuers"`
}

type issuerModel struct {
	URL                  types.String               `tfsdk:"url"`
	Audiences            []types.String             `tfsdk:"audiences"`
	AudienceMatchPolicy  types.String               `tfsdk:"audience_match_policy"`
	CertificateAuthority types.String               `tfsdk:"certificate_authority"`
	ClaimMappings        claimMappingsModel         `tfsdk:"claim_mappings"`
	ClaimValidationRules *claimValidationRulesModel `tfsdk:"claim_validation_rules"`
	UserValidationRules  []expressionRuleModel      `tfsdk:"user_validation_rules"`
}

type claimMappingsModel struct {
	Username claimMappingModel   `tfsdk:"username"`
	Groups   *groupsMappingModel `tfsdk:"groups"`
	UID      *claimMappingModel  `tfsdk:"uid"`
	Extra    []extraMappingModel `tfsdk:"extra"`
}

type claimMappingModel struct {
	Claim  types.String `tfsdk:"claim"`
	Prefix types.String `tfsdk:"prefix"`
}

type groupsMappingModel struct {
	Claim      types.String `tfsdk:"claim"`
	Expression types.String `tfsdk:"expression"`
	Prefix     types.String `tfsdk:"prefix"`
}

type extraMappingModel struct {
	Key             types.String `tfsdk:"key"`
	ValueExpression types.String `tfsdk:"value_expression"`
}

type claimValidationRulesModel struct {
	ClaimRules      []claimRuleModel      `tfsdk:"claim_rules"`
	ExpressionRules []expressionRuleModel `tfsdk:"expression_rules"`
}

type claimRuleModel struct {
	Claim         types.String `tfsdk:"claim"`
	RequiredValue types.String `tfsdk:"required_value"`
}

type expressionRuleModel struct {
	Expression types.String `tfsdk:"expression"`
	Message    types.String `tfsdk:"message"`
}

// dnsLabelPattern mirrors the API's rule for configuration names and issuer
// aliases. The spec's own pattern is unanchored, so it is anchored here.
var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

const dnsLabelMessage = "must contain only lowercase letters, digits and hyphens, and start and end with a letter or digit"

func (r *authenticationConfigurationResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *authenticationConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gardener_authentication_configuration"
}

func (r *authenticationConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	text := func(s string) schema.StringAttribute {
		return schema.StringAttribute{Required: true, MarkdownDescription: s, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}}
	}
	prefix := func(s string, modifiers ...planmodifier.String) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: s, PlanModifiers: modifiers}
	}
	expressionRules := func(s string) schema.ListNestedAttribute {
		return schema.ListNestedAttribute{
			Optional:            true,
			MarkdownDescription: s,
			Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"expression": text("CEL expression that must evaluate to true."),
				"message":    text("Message returned when the expression is false."),
			}},
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Gardener authentication configuration: the OIDC identity providers (issuers) " +
			"that Gardener clusters using it accept tokens from. Attach it to a cluster with the " +
			"`authentication_configuration_name` attribute of `cleura_gardener_shoot`. A configuration " +
			"belongs to the provider's `cloud`, not to a project, and can be used by clusters in any project.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier of the configuration: its `name`. Also the import ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the configuration: up to 32 lowercase letters, digits and hyphens, starting " +
					"and ending with a letter or digit. Changing it creates a new configuration.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 32),
					stringvalidator.RegexMatches(dnsLabelPattern, dnsLabelMessage),
				},
			},
			"issuers": schema.MapNestedAttribute{
				Required: true,
				MarkdownDescription: "The identity providers, keyed by alias: 3 to 32 lowercase letters, digits and " +
					"hyphens. The alias is part of the user and group names a cluster sees (`oidc:<alias>:`) and " +
					"selects the issuer when fetching a kubeconfig.",
				Validators: []validator.Map{
					mapvalidator.SizeAtLeast(1),
					mapvalidator.KeysAre(
						stringvalidator.LengthBetween(3, 32),
						stringvalidator.RegexMatches(dnsLabelPattern, dnsLabelMessage),
					),
				},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"url": schema.StringAttribute{
						Required: true,
						MarkdownDescription: "Issuer URL. Must use `https`, contain no username, password, query or " +
							"fragment, match the `iss` claim of the tokens exactly, and be unique within the configuration.",
					},
					"audiences": schema.ListAttribute{
						Required:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Accepted audiences, usually the OIDC client IDs. A token is accepted if its `aud` claim contains at least one of them.",
						Validators: []validator.List{
							listvalidator.SizeAtLeast(1),
							listvalidator.UniqueValues(),
							listvalidator.ValueStringsAre(stringvalidator.LengthAtLeast(1)),
						},
					},
					"audience_match_policy": schema.StringAttribute{
						Optional: true,
						Computed: true,
						Default:  stringdefault.StaticString(string(api.MatchAny)),
						MarkdownDescription: "How `audiences` is matched against a token's `aud` claim. Only `MatchAny` " +
							"(the default) is supported: the claim must contain at least one of the audiences.",
						Validators: []validator.String{stringvalidator.OneOf(string(api.MatchAny))},
					},
					"certificate_authority": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "PEM-encoded CA certificate(s) used to verify the issuer's TLS certificate, " +
							"for example `file(\"ca.pem\")`. Leave it out when the issuer uses a publicly trusted certificate.",
						Validators: []validator.String{pemCertificatesValidator{}},
					},
					"claim_mappings": schema.SingleNestedAttribute{
						Required:            true,
						MarkdownDescription: "How token claims become the Kubernetes user.",
						Attributes: map[string]schema.Attribute{
							"username": schema.SingleNestedAttribute{
								Required:            true,
								MarkdownDescription: "The claim that becomes the user name.",
								Attributes: map[string]schema.Attribute{
									"claim":  text("Claim to use, e.g. `email` or `preferred_username`."),
									"prefix": prefix("Prefix added to the user name, set by Cleura (`oidc:<alias>:`).", stringplanmodifier.UseStateForUnknown()),
								},
							},
							"groups": schema.SingleNestedAttribute{
								Optional:            true,
								MarkdownDescription: "Where the user's groups come from: exactly one of `claim` or `expression`.",
								Attributes: map[string]schema.Attribute{
									"claim": schema.StringAttribute{
										Optional:            true,
										MarkdownDescription: "Claim that holds the groups, e.g. `groups`.",
										Validators: []validator.String{
											stringvalidator.LengthAtLeast(1),
											stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("expression")),
										},
									},
									"expression": schema.StringAttribute{
										Optional:            true,
										MarkdownDescription: "CEL expression that yields the groups.",
										Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
									},
									"prefix": prefix("Prefix added to group names, set by Cleura: `oidc:<alias>:` when groups come from a `claim`, none when they come from an `expression`.", groupsPrefixModifier{}),
								},
							},
							"uid": schema.SingleNestedAttribute{
								Optional:            true,
								MarkdownDescription: "The claim that becomes the user's UID.",
								Attributes: map[string]schema.Attribute{
									"claim":  text("Claim to use, e.g. `sub`."),
									"prefix": prefix("Prefix added to the UID, set by Cleura.", stringplanmodifier.UseStateForUnknown()),
								},
							},
							"extra": schema.ListNestedAttribute{
								Optional:            true,
								MarkdownDescription: "Extra attributes added to the user.",
								Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
								NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
									"key":              text("Attribute key, a lowercase domain-prefixed path such as `example.com/team`."),
									"value_expression": text("CEL expression that yields the value."),
								}},
							},
						},
					},
					"claim_validation_rules": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "Rules a token's claims must satisfy.",
						Attributes: map[string]schema.Attribute{
							"claim_rules": schema.ListNestedAttribute{
								Optional:            true,
								MarkdownDescription: "Claims that must have a given value.",
								Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
								NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
									"claim":          text("Claim name."),
									"required_value": text("Value the claim must have."),
								}},
							},
							"expression_rules": expressionRules("CEL expressions over the claims that must evaluate to true."),
						},
					},
					"user_validation_rules": expressionRules("CEL expressions over the resulting user that must evaluate to true."),
				}},
			},
		},
	}
}

// ValidateConfig rejects, at plan time, issuer settings the API accepts today
// but Kubernetes refuses when a cluster uses the configuration, so the error
// shows up before anything is created instead of on the cluster later.
func (r *authenticationConfigurationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var issuers types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("issuers"), &issuers)...)
	if resp.Diagnostics.HasError() || issuers.IsNull() || issuers.IsUnknown() {
		return
	}
	seenURLs := map[string]string{}
	aliases := make([]string, 0, len(issuers.Elements()))
	for alias := range issuers.Elements() {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		obj, ok := issuers.Elements()[alias].(types.Object)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			continue
		}
		urlValue, ok := obj.Attributes()["url"].(types.String)
		if !ok || urlValue.IsNull() || urlValue.IsUnknown() {
			continue
		}
		at := path.Root("issuers").AtMapKey(alias).AtName("url")
		if msg := issuerURLProblem(urlValue.ValueString()); msg != "" {
			resp.Diagnostics.AddAttributeError(at, "Invalid issuer URL", msg)
			continue
		}
		if other, dup := seenURLs[urlValue.ValueString()]; dup {
			resp.Diagnostics.AddAttributeError(at, "Duplicate issuer URL",
				fmt.Sprintf("Issuers %q and %q have the same URL. Kubernetes accepts each issuer URL only once per configuration; "+
					"use one issuer with several audiences instead.", other, alias))
			continue
		}
		seenURLs[urlValue.ValueString()] = alias
	}
}

// issuerURLProblem reports why Kubernetes would reject an issuer URL, or "".
func issuerURLProblem(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Sprintf("%q is not a valid URL: %s", raw, err)
	case u.Scheme != "https":
		return fmt.Sprintf("%q must use https.", raw)
	case u.Host == "":
		return fmt.Sprintf("%q has no host.", raw)
	case u.User != nil:
		return fmt.Sprintf("%q must not contain a username or password.", raw)
	case u.RawQuery != "" || u.ForceQuery:
		return fmt.Sprintf("%q must not contain a query.", raw)
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return fmt.Sprintf("%q must not contain a fragment.", raw)
	}
	return ""
}

func (r *authenticationConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var plan authConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := api.GardenerIdentityProviderCreateAuthenticationConfigurationJSONRequestBody{
		Name:    plan.Name.ValueString(),
		Issuers: issuersToAPI(plan.Issuers),
	}
	response, err := r.config.Client.GardenerIdentityProviderCreateAuthenticationConfiguration(ctx, r.config.Cloud, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create authentication configuration", err.Error())
		return
	}
	var created api.GardenerIdentityProviderAuthenticationConfiguration
	if err := decodeJSON(response, &created); err != nil {
		detail := err.Error()
		if response.StatusCode == http.StatusConflict {
			detail += fmt.Sprintf("\n\nTo manage the existing configuration with Terraform, import it with the ID %q.", body.Name)
		}
		resp.Diagnostics.AddError("Failed to create authentication configuration", detail)
		return
	}
	resp.Diagnostics.Append(setAuthConfigState(&plan, &created)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *authenticationConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var state authConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	got, err := r.get(ctx, state.Name.ValueString())
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read authentication configuration", err.Error())
		return
	}
	resp.Diagnostics.Append(setAuthConfigState(&state, got)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *authenticationConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var plan authConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The update replaces all issuers, so the full set is sent every time.
	body := api.GardenerIdentityProviderUpdateAuthenticationConfigurationJSONRequestBody{Issuers: issuersToAPI(plan.Issuers)}
	response, err := r.config.Client.GardenerIdentityProviderUpdateAuthenticationConfiguration(ctx, r.config.Cloud, plan.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update authentication configuration", err.Error())
		return
	}
	var updated api.GardenerIdentityProviderAuthenticationConfiguration
	if err := decodeJSON(response, &updated); err != nil {
		resp.Diagnostics.AddError("Failed to update authentication configuration", err.Error())
		return
	}
	resp.Diagnostics.Append(setAuthConfigState(&plan, &updated)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *authenticationConfigurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}
	var state authConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()
	response, err := r.config.Client.GardenerIdentityProviderDeleteAuthenticationConfiguration(ctx, r.config.Cloud, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete authentication configuration", err.Error())
		return
	}
	err = decodeJSON(response, nil)
	if err == nil || errors.Is(err, errNotFound) {
		return
	}
	detail := err.Error()
	if response.StatusCode == http.StatusConflict {
		if users := r.shootsUsing(ctx, name); users != "" {
			detail += "\n\nIt is used by: " + users + ". Remove `authentication_configuration_name` from those clusters " +
				"first. If this apply moves them to another configuration, add `lifecycle { create_before_destroy = true }` " +
				"to this resource, so the clusters are updated before the old configuration is deleted."
		}
	}
	resp.Diagnostics.AddError("Failed to delete authentication configuration", detail)
}

func (r *authenticationConfigurationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

func (r *authenticationConfigurationResource) get(ctx context.Context, name string) (*api.GardenerIdentityProviderAuthenticationConfiguration, error) {
	response, err := r.config.Client.GardenerIdentityProviderGetAuthenticationConfiguration(ctx, r.config.Cloud, name)
	if err != nil {
		return nil, err
	}
	var got api.GardenerIdentityProviderAuthenticationConfiguration
	if err := decodeJSON(response, &got); err != nil {
		return nil, err
	}
	return &got, nil
}

// shootsUsing lists the clusters that use a configuration, for an error
// message; it returns "" if the list cannot be fetched.
func (r *authenticationConfigurationResource) shootsUsing(ctx context.Context, name string) string {
	response, err := r.config.Client.GardenerIdentityProviderListShootsForAuthenticationConfiguration(ctx, r.config.Cloud, name)
	if err != nil {
		return ""
	}
	var shoots []api.GardenerIdentityProviderShootReference
	if decodeJSON(response, &shoots) != nil {
		return ""
	}
	names := make([]string, 0, len(shoots))
	for _, s := range shoots {
		names = append(names, fmt.Sprintf("%s (project %s, region %s)", s.Name, s.OpenstackProjectId, s.OpenstackRegionTag))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ----- conversion -----

// issuersToAPI converts the configured issuers to the API's list, sorted by
// alias so the same configuration always produces the same request.
func issuersToAPI(issuers map[string]issuerModel) []api.GardenerIdentityProviderIssuer {
	aliases := make([]string, 0, len(issuers))
	for alias := range issuers {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	out := make([]api.GardenerIdentityProviderIssuer, 0, len(aliases))
	for _, alias := range aliases {
		in := issuers[alias]
		policy := api.GardenerAudienceMatchPolicy(in.AudienceMatchPolicy.ValueString())
		iss := api.GardenerIdentityProviderIssuer{
			Alias:               alias,
			Url:                 in.URL.ValueString(),
			Audiences:           stringsOf(in.Audiences),
			AudienceMatchPolicy: &policy,
			ClaimMappings: api.GardenerIdentityProviderClaimMappings{
				Username: api.GardenerIdentityProviderClaimMapping{Claim: in.ClaimMappings.Username.Claim.ValueString()},
			},
		}
		if in.AudienceMatchPolicy.IsNull() || in.AudienceMatchPolicy.IsUnknown() {
			iss.AudienceMatchPolicy = nil
		}
		if !in.CertificateAuthority.IsNull() {
			// The API takes the PEM base64-encoded; the exact configured bytes are sent.
			ca := base64.StdEncoding.EncodeToString([]byte(in.CertificateAuthority.ValueString()))
			iss.CertificateAuthority = &ca
		}
		if g := in.ClaimMappings.Groups; g != nil {
			iss.ClaimMappings.Groups = &api.GardenerIdentityProviderClaimWithExpressionMapping{
				Claim:      g.Claim.ValueStringPointer(),
				Expression: g.Expression.ValueStringPointer(),
			}
		}
		if u := in.ClaimMappings.UID; u != nil {
			iss.ClaimMappings.Uid = &api.GardenerIdentityProviderClaimMapping{Claim: u.Claim.ValueString()}
		}
		if len(in.ClaimMappings.Extra) > 0 {
			extra := make([]api.GardenerIdentityProviderExtraClaimMapping, 0, len(in.ClaimMappings.Extra))
			for _, e := range in.ClaimMappings.Extra {
				extra = append(extra, api.GardenerIdentityProviderExtraClaimMapping{Key: e.Key.ValueString(), ValueExpression: e.ValueExpression.ValueString()})
			}
			iss.ClaimMappings.Extra = &extra
		}
		if v := in.ClaimValidationRules; v != nil {
			rules := &api.GardenerIdentityProviderClaimValidationRules{}
			if len(v.ClaimRules) > 0 {
				claimRules := make([]api.GardenerIdentityProviderClaimValidationRule, 0, len(v.ClaimRules))
				for _, c := range v.ClaimRules {
					claimRules = append(claimRules, api.GardenerIdentityProviderClaimValidationRule{Claim: c.Claim.ValueString(), RequiredValue: c.RequiredValue.ValueString()})
				}
				rules.ClaimRules = &claimRules
			}
			if len(v.ExpressionRules) > 0 {
				expressionRules := make([]api.GardenerIdentityProviderExpressionValidationRule, 0, len(v.ExpressionRules))
				for _, e := range v.ExpressionRules {
					expressionRules = append(expressionRules, api.GardenerIdentityProviderExpressionValidationRule{Expression: e.Expression.ValueString(), Message: e.Message.ValueString()})
				}
				rules.ExpressionRules = &expressionRules
			}
			iss.ClaimValidationRules = rules
		}
		if len(in.UserValidationRules) > 0 {
			userRules := make([]api.GardenerIdentityProviderUserValidationRule, 0, len(in.UserValidationRules))
			for _, u := range in.UserValidationRules {
				userRules = append(userRules, api.GardenerIdentityProviderUserValidationRule{Expression: u.Expression.ValueString(), Message: u.Message.ValueString()})
			}
			iss.UserValidationRules = &userRules
		}
		out = append(out, iss)
	}
	return out
}

// setAuthConfigState copies the API's view of a configuration into the model.
//
// The CA comes back base64-encoded. When it decodes to exactly what is in the
// model already, the model's text is kept as written; otherwise (an import, or
// a change made elsewhere) the decoded PEM is stored.
func setAuthConfigState(m *authConfigModel, c *api.GardenerIdentityProviderAuthenticationConfiguration) diag.Diagnostics {
	var diags diag.Diagnostics
	prior := m.Issuers
	m.ID = types.StringValue(c.Name)
	m.Name = types.StringValue(c.Name)
	m.Issuers = make(map[string]issuerModel, len(c.Issuers))
	for _, iss := range c.Issuers {
		if _, dup := m.Issuers[iss.Alias]; dup {
			diags.AddError("Unexpected authentication configuration",
				fmt.Sprintf("The API returned two issuers with the alias %q.", iss.Alias))
			continue
		}
		im := issuerModel{
			URL:                 types.StringValue(iss.Url),
			Audiences:           stringValuesOf(iss.Audiences),
			AudienceMatchPolicy: types.StringValue(string(api.MatchAny)),
			ClaimMappings: claimMappingsModel{
				Username: claimMappingModel{Claim: types.StringValue(iss.ClaimMappings.Username.Claim), Prefix: types.StringPointerValue(iss.ClaimMappings.Username.Prefix)},
			},
		}
		if iss.AudienceMatchPolicy != nil {
			im.AudienceMatchPolicy = types.StringValue(string(*iss.AudienceMatchPolicy))
		}
		im.CertificateAuthority = types.StringNull()
		if iss.CertificateAuthority != nil && *iss.CertificateAuthority != "" {
			decoded, err := base64.StdEncoding.DecodeString(*iss.CertificateAuthority)
			if err != nil {
				diags.AddError("Unexpected authentication configuration",
					fmt.Sprintf("The certificate authority of issuer %q is not valid base64: %s", iss.Alias, err))
				continue
			}
			im.CertificateAuthority = types.StringValue(string(decoded))
			if p, ok := prior[iss.Alias]; ok && p.CertificateAuthority.ValueString() == string(decoded) {
				im.CertificateAuthority = p.CertificateAuthority
			}
		}
		if g := iss.ClaimMappings.Groups; g != nil {
			im.ClaimMappings.Groups = &groupsMappingModel{
				Claim:      types.StringPointerValue(g.Claim),
				Expression: types.StringPointerValue(g.Expression),
				Prefix:     types.StringPointerValue(g.Prefix),
			}
		}
		if u := iss.ClaimMappings.Uid; u != nil {
			im.ClaimMappings.UID = &claimMappingModel{Claim: types.StringValue(u.Claim), Prefix: types.StringPointerValue(u.Prefix)}
		}
		if e := iss.ClaimMappings.Extra; e != nil && len(*e) > 0 {
			for _, x := range *e {
				im.ClaimMappings.Extra = append(im.ClaimMappings.Extra, extraMappingModel{Key: types.StringValue(x.Key), ValueExpression: types.StringValue(x.ValueExpression)})
			}
		}
		if v := iss.ClaimValidationRules; v != nil {
			rules := &claimValidationRulesModel{}
			if v.ClaimRules != nil {
				for _, c := range *v.ClaimRules {
					rules.ClaimRules = append(rules.ClaimRules, claimRuleModel{Claim: types.StringValue(c.Claim), RequiredValue: types.StringValue(c.RequiredValue)})
				}
			}
			if v.ExpressionRules != nil {
				for _, x := range *v.ExpressionRules {
					rules.ExpressionRules = append(rules.ExpressionRules, expressionRuleModel{Expression: types.StringValue(x.Expression), Message: types.StringValue(x.Message)})
				}
			}
			// An object with no rules reads as no object, matching a configuration
			// that never sets claim_validation_rules.
			if len(rules.ClaimRules) > 0 || len(rules.ExpressionRules) > 0 {
				im.ClaimValidationRules = rules
			}
		}
		if u := iss.UserValidationRules; u != nil {
			for _, x := range *u {
				im.UserValidationRules = append(im.UserValidationRules, expressionRuleModel{Expression: types.StringValue(x.Expression), Message: types.StringValue(x.Message)})
			}
		}
		m.Issuers[iss.Alias] = im
	}
	return diags
}

func stringsOf(values []types.String) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v.ValueString())
	}
	return out
}

func stringValuesOf(values []string) []types.String {
	out := make([]types.String, 0, len(values))
	for _, v := range values {
		out = append(out, types.StringValue(v))
	}
	return out
}

// ----- plan modifiers and validators -----

// groupsPrefixModifier plans the group prefix the API will set: none when the
// groups come from an expression, and the previous prefix while they keep
// coming from a claim. Anything else stays unknown until the API reports it.
// A plain UseStateForUnknown would carry a claim's prefix into a switch to an
// expression, which the API then answers with no prefix.
type groupsPrefixModifier struct{}

func (groupsPrefixModifier) Description(context.Context) string {
	return "Plans the group prefix set by Cleura."
}

func (m groupsPrefixModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (groupsPrefixModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.PlanValue.IsUnknown() {
		return
	}
	var expression types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, req.Path.ParentPath().AtName("expression"), &expression)...)
	if resp.Diagnostics.HasError() || expression.IsUnknown() {
		return
	}
	if !expression.IsNull() {
		resp.PlanValue = types.StringNull()
		return
	}
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	var priorExpression types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, req.Path.ParentPath().AtName("expression"), &priorExpression)...)
	if !resp.Diagnostics.HasError() && priorExpression.IsNull() {
		resp.PlanValue = req.StateValue
	}
}

// pemCertificatesValidator requires one or more PEM certificates and nothing else.
type pemCertificatesValidator struct{}

func (pemCertificatesValidator) Description(context.Context) string {
	return "must be one or more PEM-encoded certificates"
}

func (v pemCertificatesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v pemCertificatesValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	rest := []byte(req.ConfigValue.ValueString())
	count := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid certificate authority",
				fmt.Sprintf("Expected only PEM certificates, found a %q block.", block.Type))
			return
		}
		count++
	}
	if count == 0 || strings.TrimSpace(string(rest)) != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid certificate authority",
			"Expected one or more PEM-encoded certificates (\"-----BEGIN CERTIFICATE-----\" ...), for example file(\"ca.pem\"). "+
				"Do not base64-encode it yourself; the provider does that.")
	}
}
