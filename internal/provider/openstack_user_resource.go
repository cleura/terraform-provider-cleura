package provider

import (
	"context"
	"errors"
	"regexp"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*openstackUserResource)(nil)
	_ resource.ResourceWithImportState = (*openstackUserResource)(nil)
)

func NewOpenStackUserResource() resource.Resource {
	return &openstackUserResource{}
}

type openstackUserResource struct {
	config *ProviderConfig
}

type openstackUserModel struct {
	ID                types.String `tfsdk:"id"`
	DomainID          types.String `tfsdk:"domain_id"`
	Name              types.String `tfsdk:"name"`
	Password          types.String `tfsdk:"password"`
	PasswordWoVersion types.String `tfsdk:"password_wo_version"`
	Description       types.String `tfsdk:"description"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	DefaultProjectID  types.String `tfsdk:"default_project_id"`
}

// openstackUserNamePattern mirrors the API's constraint on user names.
var openstackUserNamePattern = regexp.MustCompile(`^[0-9a-z_.\-]{3,40}$`)

func (r *openstackUserResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *openstackUserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_openstack_user"
}

func (r *openstackUserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an OpenStack (Keystone) user in your Cleura account. OpenStack users " +
			"authenticate against OpenStack itself (the OpenStack CLI, the OpenStack Terraform provider, ...) " +
			"and are distinct from Cleura account users. Users live in an OpenStack domain; by default the " +
			"provider uses the domain that serves the provider's `region`, and `domain_id` overrides that.\n\n" +
			"The password is a write-only argument: it is sent to the API but never stored in state (requires " +
			"Terraform 1.11 or later). Change `password_wo_version` to make the next apply re-send it.\n\n" +
			"A new user has no access to any project; grant project roles with `cleura_openstack_role_assignment`. " +
			"Import an existing user with its ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "OpenStack user ID (32 hex characters), assigned by Cleura. Also the import ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"domain_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack domain the user is created in. Defaults to the account's domain that " +
					"serves the provider's `region` (or the account's only domain). Set it explicitly when the " +
					"account has several domains for the region; list them with `cleura openstack domain list`. " +
					"Changing it forces a new user.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "User name: 3 to 40 characters of lowercase letters, digits, and `_ . -`. Must be " +
					"unique within the domain. Updated in place.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(openstackUserNamePattern, "must be 3-40 characters of lowercase letters, digits, and _ . -"),
				},
			},
			"password": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				WriteOnly: true,
				MarkdownDescription: "Password for the user, 8 to 1024 characters. Write-only: sent to the API on " +
					"create and whenever `password_wo_version` changes, never stored in state. Requires Terraform 1.11+.\n\n" +
					"The API accepts letters, numbers and the special characters ``@#!£&?<>;:.-[](){}+%\"'=^*$`` and " +
					"space. Note that `/` is **not** accepted, so a password from `openssl rand -base64` is rejected " +
					"at apply time; use `openssl rand -hex` or a generator restricted to the set above.",
				Validators: []validator.String{stringvalidator.LengthBetween(8, 1024)},
			},
			"password_wo_version": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Arbitrary version marker for the write-only `password`. Because the password is " +
					"not stored in state, Terraform cannot detect that it changed; bump this value (e.g. `\"2\"`) to " +
					"re-send the current `password` on the next apply.",
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Free-text description. Removing it from the configuration clears it on the " +
					"user. Updated in place. The API accepts only letters, numbers, spaces and the punctuation " +
					"`. , _ : / -` — other characters are rejected at apply time.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether the user can log in. Defaults to `true`. Updated in place.",
			},
			"default_project_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The user's default OpenStack project, if Cleura has assigned one (read-only).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *openstackUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan, config openstackUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// The write-only password is only present in the configuration.
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, plan.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	// API WORKAROUND: the create endpoint accepts a description but does not
	// store it, while PATCH does. So create with name and password only and
	// apply description (and a planned enabled = false, which create cannot set
	// either) in one follow-up edit.
	body := api.OpenStackIdentityCreateUserRequest{
		Name:     plan.Name.ValueString(),
		Password: config.Password.ValueString(),
	}
	response, err := r.config.Client.OpenStackIdentityCreateUser(ctx, domainID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create OpenStack user", redactSecret(err.Error(), body.Password))
		return
	}
	var created api.OpenStackIdentityUserWithProjectsAccess
	if err := decodeJSONRedacting(response, &created, body.Password); err != nil {
		resp.Diagnostics.AddError("Failed to create OpenStack user", redactSecret(err.Error(), body.Password))
		return
	}

	// The user now exists. Persist state before the follow-up call so a
	// failure below leaves a tracked resource rather than an orphan.
	setOpenStackUserState(&plan, userFromAccessView(&created))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Description and enabled were read back from the create response above;
	// compare against the request to see whether the follow-up is needed.
	var followUp api.OpenStackIdentityEditUserRequest
	needed := false
	if !req.Plan.Raw.IsNull() {
		var wanted openstackUserModel
		resp.Diagnostics.Append(req.Plan.Get(ctx, &wanted)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !wanted.Description.IsNull() && !wanted.Description.Equal(plan.Description) {
			followUp.Description = wanted.Description.ValueStringPointer()
			needed = true
		}
		if !wanted.Enabled.ValueBool() {
			disabled := false
			followUp.Enabled = &disabled
			needed = true
		}
	}
	if needed {
		updated, err := r.edit(ctx, domainID, created.Id, followUp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to apply the OpenStack user's description or enabled state after creating it", err.Error())
			return
		}
		setOpenStackUserState(&plan, updated)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *openstackUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state openstackUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	users, err := listUsers(ctx, r.config, domainID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read OpenStack user", err.Error())
		return
	}
	user, found := findUserByID(users, state.ID.ValueString())
	if !found {
		// Deleted outside Terraform; drop it so the next plan recreates it.
		resp.State.RemoveResource(ctx)
		return
	}

	setOpenStackUserState(&state, user)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *openstackUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan, state, config openstackUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	// PATCH merges: send only what changed. A removed description is cleared
	// by sending "" (an omitted field would be left untouched). The password
	// is re-sent only when its version marker changed, so unrelated edits do
	// not reset it.
	body := api.OpenStackIdentityEditUserRequest{}
	changed := false
	if !plan.Name.Equal(state.Name) {
		body.Name = plan.Name.ValueStringPointer()
		changed = true
	}
	if !plan.Description.Equal(state.Description) {
		description := plan.Description.ValueString()
		body.Description = &description
		changed = true
	}
	if !plan.Enabled.Equal(state.Enabled) {
		body.Enabled = plan.Enabled.ValueBoolPointer()
		changed = true
	}
	if !plan.PasswordWoVersion.Equal(state.PasswordWoVersion) && !config.Password.IsNull() {
		body.Password = config.Password.ValueStringPointer()
		changed = true
	}
	if !changed {
		plan.ID, plan.DomainID, plan.DefaultProjectID = state.ID, state.DomainID, state.DefaultProjectID
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	user, err := r.edit(ctx, domainID, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update OpenStack user", redactSecret(err.Error(), config.Password.ValueString()))
		return
	}
	setOpenStackUserState(&plan, user)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *openstackUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state openstackUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}

	response, err := r.config.Client.OpenStackIdentityDeleteUser(ctx, domainID, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete OpenStack user", err.Error())
		return
	}
	err = decodeJSON(response, nil)
	if errors.Is(err, errNotFound) {
		// Already gone: the destroy goal is met.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete OpenStack user", err.Error())
	}
}

func (r *openstackUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// edit PATCHes the user and returns the API's view of it.
func (r *openstackUserResource) edit(ctx context.Context, domainID, id string, body api.OpenStackIdentityEditUserRequest) (*api.OpenStackIdentityUser, error) {
	response, err := r.config.Client.OpenStackIdentityEditUser(ctx, domainID, id, body)
	if err != nil {
		return nil, err
	}
	var user api.OpenStackIdentityUserWithProjectsAccess
	// body.Password is set only when the password is being rotated; an empty
	// secret leaves the error text untouched.
	if err := decodeJSONRedacting(response, &user, derefOrEmpty(body.Password)); err != nil {
		return nil, err
	}
	return userFromAccessView(&user), nil
}

// setOpenStackUserState copies the API's user into the model. The write-only
// password stays null (the framework requires it to be absent from state).
// derefOrEmpty reads an optional request field without a nil check at each use.
func derefOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func setOpenStackUserState(m *openstackUserModel, u *api.OpenStackIdentityUser) {
	m.ID = types.StringValue(u.Id)
	m.DomainID = types.StringValue(u.DomainId)
	m.Name = types.StringValue(u.Name)
	m.Password = types.StringNull()
	m.Description = optionalStringValue(u.Description)
	m.Enabled = types.BoolValue(u.Enabled)
	m.DefaultProjectID = optionalStringValue(u.DefaultProjectId)
}
