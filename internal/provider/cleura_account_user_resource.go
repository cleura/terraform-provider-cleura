package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	api "github.com/cleura/terraform-provider-cleura/api"
	"github.com/cleura/terraform-provider-cleura/cleura"
)

var (
	_ resource.Resource                = (*accountUserResource)(nil)
	_ resource.ResourceWithConfigure   = (*accountUserResource)(nil)
	_ resource.ResourceWithImportState = (*accountUserResource)(nil)
)

func NewAccountUserResource() resource.Resource {
	return &accountUserResource{}
}

type accountUserResource struct {
	config *cleura.ProviderConfig
}

type accountUserResourceModel struct {
	ID                types.String                `tfsdk:"id"`
	Username          types.String                `tfsdk:"username"`
	Email             types.String                `tfsdk:"email"`
	Password          types.String                `tfsdk:"password"`
	PasswordWOVersion types.String                `tfsdk:"password_wo_version"`
	Firstname         types.String                `tfsdk:"firstname"`
	Lastname          types.String                `tfsdk:"lastname"`
	IPRestrictions    []types.String              `tfsdk:"ip_restrictions"`
	Privileges        *accountUserPrivilegesModel `tfsdk:"privileges"`
	Admin             types.Bool                  `tfsdk:"admin"`
	PendingEmail      types.String                `tfsdk:"pending_email"`
	Language          types.String                `tfsdk:"language"`
}

type accountUserPrivilegesModel struct {
	Account     *accountUserPrivilegeModel `tfsdk:"account"`
	AiGateway   *accountUserPrivilegeModel `tfsdk:"ai_gateway"`
	Application *accountUserPrivilegeModel `tfsdk:"application"`
	Invoice     *accountUserPrivilegeModel `tfsdk:"invoice"`
	Monitoring  *accountUserPrivilegeModel `tfsdk:"monitoring"`
	Openstack   *accountUserPrivilegeModel `tfsdk:"openstack"`
	Users       *accountUserPrivilegeModel `tfsdk:"users"`
}

type accountUserPrivilegeModel struct {
	Type              types.String                       `tfsdk:"type"`
	Meta              types.String                       `tfsdk:"meta"`
	ProjectPrivileges []accountUserProjectPrivilegeModel `tfsdk:"project_privileges"`
}

type accountUserProjectPrivilegeModel struct {
	DomainID  types.String `tfsdk:"domain_id"`
	ProjectID types.String `tfsdk:"project_id"`
	Type      types.String `tfsdk:"type"`
}

func (r *accountUserResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *accountUserResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_account_user"
}

func (r *accountUserResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	privilegeTypeValidator := []validator.String{stringvalidator.OneOf("full", "read", "project")}

	resp.Schema = schema.Schema{
		Description: "Manages a Cleura Cloud Management System account user, including its privileges. " +
			"Privileges are a field of the user object in the Cleura API (set on create/edit), so they are " +
			"managed inline here rather than as a separate resource. " +
			"Account-scoped: requires only provider credentials (cloud, username, token).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Numeric account user id assigned by Cleura.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"username": schema.StringAttribute{
				Required:    true,
				Description: "Login username. 3-40 characters of [0-9a-z_.-].",
			},
			"email": schema.StringAttribute{
				Required:    true,
				Description: "Email address of the account user.",
			},
			"password": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				WriteOnly:   true,
				Description: "Account user password. Write-only: it is sent to the API but never stored in Terraform state (requires Terraform 1.11+). Change password_wo_version to force the password to be re-sent.",
			},
			"password_wo_version": schema.StringAttribute{
				Optional:    true,
				Description: "Arbitrary version token. Changing this value triggers an update that re-sends the write-only password. Use it to rotate the password.",
			},
			"firstname": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "First name of the account user. The Cleura API cannot clear it once set, " +
					"so removing it from configuration leaves the last value in place (a no-op) rather " +
					"than erroring.",
			},
			"lastname": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "Last name of the account user. Behaves like firstname (same field type and " +
					"merge handling): removing it from configuration is a no-op, not an error.",
			},
			// ip_restrictions uses replace semantics like privileges (verified against
			// the live API: removing it clears the list). So it is a plain Optional
			// field -- omitting it sends nothing and the API clears the list. This
			// differs from the scalar fields (firstname/lastname), which use merge
			// semantics and need Computed+UseStateForUnknown to make removal a no-op
			// instead of an "inconsistent result after apply" error.
			"ip_restrictions": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Set of CIDRs the account user is allowed to log in from. Omit on create " +
					"to allow all; removing it later clears the list (the API replaces it wholesale).",
			},
			"admin": schema.BoolAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				Description:   "Whether the account user is an account administrator (server-managed).",
			},
			"pending_email": schema.StringAttribute{
				Computed: true,
				Description: "Pending email address awaiting verification, if any (server-managed). Not " +
					"pinned across updates because it can change when email changes.",
			},
			"language": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description:   "UI language of the account user (server-managed).",
			},
			"privileges": schema.SingleNestedAttribute{
				Optional: true,
				Description: "Privilege matrix for the account user. Each category grants an access level " +
					"(full, read, or project). This is how roles are assigned to account users. Removing a " +
					"category — or the whole block — revokes that access (the API replaces privileges " +
					"wholesale).",
				Attributes: map[string]schema.Attribute{
					"account":     privilegeSchema("account", privilegeTypeValidator),
					"ai_gateway":  privilegeSchema("AI gateway", privilegeTypeValidator),
					"application": privilegeSchema("application", privilegeTypeValidator),
					"invoice":     privilegeSchema("invoice", privilegeTypeValidator),
					"monitoring":  privilegeSchema("monitoring", privilegeTypeValidator),
					"openstack":   privilegeSchema("OpenStack", privilegeTypeValidator),
					"users":       privilegeSchema("users", privilegeTypeValidator),
				},
			},
		},
	}
}

func privilegeSchema(area string, typeValidator []validator.String) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: fmt.Sprintf("%s privilege for the account user.", area),
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				Required:    true,
				Description: "Access level: full, read, or project.",
				Validators:  typeValidator,
			},
			// `meta` is marked required on every privilege object in the Cleura
			// API but is undocumented (no defined values). Empirically the API
			// stores and returns an empty string for it across all categories, so
			// the provider defaults it to "" and treats it as an optional
			// passthrough: omit it and "" is sent. It is Computed so the value the
			// API returns wins on refresh.
			//
			// If the API ever starts deriving or normalizing `meta` server-side
			// (returns something other than what was sent), Terraform will report
			// "inconsistent result after apply" — the signal that `meta` should be
			// modeled fully read-only. See cleura-api-wishlist.md item 11.
			"meta": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(""),
				Description: "Opaque metadata for this privilege. Undocumented in the Cleura API and " +
					"empirically always an empty string; defaults to \"\" and is passed through verbatim. " +
					"See the provider API wishlist (item 11).",
			},
			"project_privileges": schema.SetNestedAttribute{
				Optional:    true,
				Description: "Per-project access grants (OpenStack project scoping).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"domain_id": schema.StringAttribute{
							Required:    true,
							Description: "OpenStack domain id (stripped UUID, 32 hex chars).",
						},
						"project_id": schema.StringAttribute{
							Required:    true,
							Description: "OpenStack project id (stripped UUID, 32 hex chars).",
						},
						"type": schema.StringAttribute{
							Required:    true,
							Description: "Access level for this project: full, read, or project.",
							Validators:  typeValidator,
						},
					},
				},
			},
		},
	}
}

func (r *accountUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan accountUserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// password is write-only: it is present in config, not in plan/state.
	var config accountUserResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := api.IdentityCreateUserRequest{
		Username:   plan.Username.ValueString(),
		Email:      plan.Email.ValueString(),
		Password:   config.Password.ValueString(),
		Firstname:  optionalString(plan.Firstname),
		Lastname:   optionalString(plan.Lastname),
		Privileges: privilegesToAPI(plan.Privileges),
	}
	if ips := ipRestrictionsToAPI(plan.IPRestrictions); ips != nil {
		createReq.IpRestrictions = ips
	}

	response, err := r.config.Client.IdentityCreateUser(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create account user", err.Error())
		return
	}
	user, ok := decodeUserResponse(response, &resp.Diagnostics, "create account user")
	if !ok {
		return
	}

	mapUserToModel(user, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state accountUserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	user, found, ok := findAccountUserByID(ctx, r.config, state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	if !found {
		// User no longer exists; remove from state so Terraform plans a recreate.
		resp.State.RemoveResource(ctx)
		return
	}

	mapUserToModel(user, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *accountUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan accountUserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// id is computed and comes from prior state.
	var state accountUserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// password is write-only: read it from config, not plan/state.
	var config accountUserResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The Cleura API uses replace semantics for privileges: it clears whatever is
	// not in the request -- a single omitted category, or all privileges when the
	// whole block is removed (privilegesToAPI returns nil and the field is omitted).
	// So removal works in every form with no special handling here; the
	// privilegesToAPI call below covers it. Verified against the live API:
	// commenting out the whole privileges block clears all privileges cleanly.
	// See cleura-api-wishlist.md item 12 (the behavior works but is undocumented).
	editReq := api.IdentityEditUserRequest{
		Username:   plan.Username.ValueStringPointer(),
		Email:      plan.Email.ValueStringPointer(),
		Firstname:  optionalString(plan.Firstname),
		Lastname:   optionalString(plan.Lastname),
		Privileges: privilegesToAPI(plan.Privileges),
	}
	if ips := ipRestrictionsToAPI(plan.IPRestrictions); ips != nil {
		editReq.IpRestrictions = ips
	}
	if !config.Password.IsNull() {
		editReq.Password = config.Password.ValueStringPointer()
	}

	response, err := r.config.Client.IdentityEditUser(ctx, state.ID.ValueString(), editReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update account user", err.Error())
		return
	}
	user, ok := decodeUserResponse(response, &resp.Diagnostics, "update account user")
	if !ok {
		return
	}

	mapUserToModel(user, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accountUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state accountUserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	response, err := r.config.Client.IdentityDeleteUser(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete account user", err.Error())
		return
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(response.Body)
	// Treat 404 as already deleted.
	if response.StatusCode == http.StatusNotFound {
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		resp.Diagnostics.AddError(fmt.Sprintf("API error %d deleting account user", response.StatusCode), string(body))
		return
	}
}

func (r *accountUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// decodeUserResponse reads and validates a CommonUserLogin response body.
func decodeUserResponse(response *http.Response, diags *diag.Diagnostics, action string) (*api.CommonUserLogin, bool) {
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		diags.AddError("Failed to read response body", err.Error())
		return nil, false
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		diags.AddError(fmt.Sprintf("API error %d (%s)", response.StatusCode, action), string(body))
		return nil, false
	}

	var user api.CommonUserLogin
	if err := json.Unmarshal(body, &user); err != nil {
		diags.AddError("Failed to unmarshal account user", err.Error())
		return nil, false
	}
	return &user, true
}

// findAccountUserByID lists account users and returns the one matching id.
// The Identity API has no single-user GET endpoint, so this lists and filters.
// Returns (user, found, ok); ok is false only when an error was recorded.
func findAccountUserByID(ctx context.Context, config *cleura.ProviderConfig, id string, diags *diag.Diagnostics) (*api.CommonUserLogin, bool, bool) {
	response, err := config.Client.IdentityListUsers(ctx)
	if err != nil {
		diags.AddError("Failed to list account users", err.Error())
		return nil, false, false
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		diags.AddError("Failed to read response body", err.Error())
		return nil, false, false
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		diags.AddError(fmt.Sprintf("API error %d listing account users", response.StatusCode), string(body))
		return nil, false, false
	}

	var users []api.CommonUserLogin
	if err := json.Unmarshal(body, &users); err != nil {
		diags.AddError("Failed to unmarshal account users", err.Error())
		return nil, false, false
	}

	for i := range users {
		if strconv.Itoa(users[i].Id) == id {
			return &users[i], true, true
		}
	}
	return nil, false, true
}

func mapUserToModel(u *api.CommonUserLogin, model *accountUserResourceModel) {
	model.ID = types.StringValue(strconv.Itoa(u.Id))
	// Workaround: the create/edit request uses `username` but the response uses
	// `name` for the same value. See cleura-api-wishlist.md item 13 (symmetric
	// request/response schema).
	model.Username = types.StringValue(u.Name)
	model.Email = types.StringPointerValue(u.Email)
	model.Firstname = types.StringPointerValue(u.Firstname)
	model.Lastname = types.StringPointerValue(u.Lastname)
	model.Admin = types.BoolValue(u.Admin)
	model.PendingEmail = types.StringPointerValue(u.PendingEmail)
	model.Language = types.StringPointerValue(u.Language)

	// Workaround: the request accepts ip_restrictions as a []string of CIDRs,
	// but the response returns objects {cidr, created}. We collapse back to the
	// CIDR list and drop the server-set `created` timestamp, since there is no
	// writable field for it. See cleura-api-wishlist.md item 13.
	if len(u.IpRestrictions) > 0 {
		ips := make([]types.String, 0, len(u.IpRestrictions))
		for _, ip := range u.IpRestrictions {
			ips = append(ips, types.StringValue(ip.Cidr))
		}
		model.IPRestrictions = ips
	} else {
		model.IPRestrictions = nil
	}

	model.Privileges = apiToPrivileges(u.Privileges)
}

func optionalString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return v.ValueStringPointer()
}

func ipRestrictionsToAPI(in []types.String) *[]string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, v.ValueString())
	}
	return &out
}

func privilegesToAPI(in *accountUserPrivilegesModel) *api.CommonUserLoginPrivileges {
	if in == nil {
		return nil
	}
	return &api.CommonUserLoginPrivileges{
		Account:     privilegeToAPI(in.Account),
		AiGateway:   privilegeToAPI(in.AiGateway),
		Application: privilegeToAPI(in.Application),
		Invoice:     privilegeToAPI(in.Invoice),
		Monitoring:  privilegeToAPI(in.Monitoring),
		Openstack:   privilegeToAPI(in.Openstack),
		Users:       privilegeToAPI(in.Users),
	}
}

func privilegeToAPI(in *accountUserPrivilegeModel) *api.CommonUserLoginPrivilege {
	if in == nil {
		return nil
	}
	out := &api.CommonUserLoginPrivilege{
		Type: api.UserUserLoginPrivilegeType(in.Type.ValueString()),
		Meta: in.Meta.ValueString(),
	}
	if len(in.ProjectPrivileges) > 0 {
		pps := make([]api.CommonUserLoginProjectPrivilege, 0, len(in.ProjectPrivileges))
		for _, pp := range in.ProjectPrivileges {
			pps = append(pps, api.CommonUserLoginProjectPrivilege{
				DomainId:  pp.DomainID.ValueString(),
				ProjectId: pp.ProjectID.ValueString(),
				Type:      api.UserUserLoginPrivilegeType(pp.Type.ValueString()),
			})
		}
		out.ProjectPrivileges = &pps
	}
	return out
}

func apiToPrivileges(in api.CommonUserLoginPrivileges) *accountUserPrivilegesModel {
	m := &accountUserPrivilegesModel{
		Account:     apiToPrivilege(in.Account),
		AiGateway:   apiToPrivilege(in.AiGateway),
		Application: apiToPrivilege(in.Application),
		Invoice:     apiToPrivilege(in.Invoice),
		Monitoring:  apiToPrivilege(in.Monitoring),
		Openstack:   apiToPrivilege(in.Openstack),
		Users:       apiToPrivilege(in.Users),
	}
	if m.Account == nil && m.AiGateway == nil && m.Application == nil && m.Invoice == nil &&
		m.Monitoring == nil && m.Openstack == nil && m.Users == nil {
		return nil
	}
	return m
}

func apiToPrivilege(in *api.CommonUserLoginPrivilege) *accountUserPrivilegeModel {
	if in == nil {
		return nil
	}
	out := &accountUserPrivilegeModel{
		Type: types.StringValue(string(in.Type)),
		Meta: types.StringValue(in.Meta),
	}
	if in.ProjectPrivileges != nil && len(*in.ProjectPrivileges) > 0 {
		for _, pp := range *in.ProjectPrivileges {
			out.ProjectPrivileges = append(out.ProjectPrivileges, accountUserProjectPrivilegeModel{
				DomainID:  types.StringValue(pp.DomainId),
				ProjectID: types.StringValue(pp.ProjectId),
				Type:      types.StringValue(string(pp.Type)),
			})
		}
	}
	return out
}
