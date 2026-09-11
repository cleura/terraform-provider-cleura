package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*openstackRoleAssignmentResource)(nil)
	_ resource.ResourceWithImportState = (*openstackRoleAssignmentResource)(nil)
)

func NewOpenStackRoleAssignmentResource() resource.Resource {
	return &openstackRoleAssignmentResource{}
}

type openstackRoleAssignmentResource struct {
	config *ProviderConfig
}

type openstackRoleAssignmentModel struct {
	ID        types.String `tfsdk:"id"`
	DomainID  types.String `tfsdk:"domain_id"`
	UserID    types.String `tfsdk:"user_id"`
	ProjectID types.String `tfsdk:"project_id"`
	Roles     types.Set    `tfsdk:"roles"`
}

func (r *openstackRoleAssignmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.config = fromResource(ctx, req, resp)
}

func (r *openstackRoleAssignmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_openstack_role_assignment"
}

func (r *openstackRoleAssignmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Grants an OpenStack user a set of roles on an OpenStack project. One resource manages " +
			"**all** of a user's roles on one project: roles added to `roles` are granted, roles removed from it " +
			"are revoked, and a role granted outside Terraform on the same user and project is revoked on the " +
			"next apply. Use one resource per user and project pair.\n\n" +
			"Roles are given by name (typically `member`; list them with `cleura openstack role list`). The user " +
			"and project must be in the same OpenStack domain; by default the provider uses the domain that " +
			"serves its `region`, and `domain_id` overrides that.\n\n" +
			"Import with `<user_id>/<project_id>` (or `<domain_id>/<user_id>/<project_id>`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Synthetic ID `<user_id>/<project_id>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"domain_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "OpenStack domain of the user and project. Defaults to the account's domain that " +
					"serves the provider's `region` (or the account's only domain). Changing it forces a new assignment.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the OpenStack user (see `cleura_openstack_user`). Changing it forces a new assignment.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"project_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "ID of the OpenStack project (see `cleura_openstack_project`). Changing it forces a new assignment.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"roles": schema.SetAttribute{
				ElementType: types.StringType,
				Required:    true,
				MarkdownDescription: "Role names the user holds on the project, e.g. `[\"member\"]`. At least one. " +
					"Changes are applied in place by granting and revoking individual roles.",
				Validators: []validator.Set{setvalidator.SizeAtLeast(1)},
			},
		},
	}
}

func (r *openstackRoleAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan openstackRoleAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, plan.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}
	var names []string
	resp.Diagnostics.Append(plan.Roles.ElementsAs(ctx, &names, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	roleIDs, err := r.roleIDs(ctx, domainID, names)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("roles"), "Unknown OpenStack role", err.Error())
		return
	}

	userID, projectID := plan.UserID.ValueString(), plan.ProjectID.ValueString()
	if err := grantProjectRoles(ctx, r.config, domainID, userID, projectID, roleIDs); err != nil {
		resp.Diagnostics.AddError("Failed to grant OpenStack project access", err.Error())
		return
	}

	plan.ID = types.StringValue(userID + "/" + projectID)
	plan.DomainID = types.StringValue(domainID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *openstackRoleAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state openstackRoleAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}
	held, err := userProjectRoles(ctx, r.config, domainID, state.UserID.ValueString(), state.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read OpenStack project access", err.Error())
		return
	}
	if len(held) == 0 {
		// All roles revoked, or the user is gone: nothing left to manage.
		resp.State.RemoveResource(ctx)
		return
	}

	roles, diags := types.SetValueFrom(ctx, types.StringType, held)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Roles = roles
	state.DomainID = types.StringValue(domainID)
	state.ID = types.StringValue(state.UserID.ValueString() + "/" + state.ProjectID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *openstackRoleAssignmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var plan, state openstackRoleAssignmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}
	var wanted, current []string
	resp.Diagnostics.Append(plan.Roles.ElementsAs(ctx, &wanted, false)...)
	resp.Diagnostics.Append(state.Roles.ElementsAs(ctx, &current, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	added, removed := setDiff(wanted, current)
	byName, err := r.config.domainRoles(ctx, domainID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list OpenStack roles", err.Error())
		return
	}
	userID, projectID := state.UserID.ValueString(), state.ProjectID.ValueString()

	// Grant first so the user never loses all access mid-update.
	if len(added) > 0 {
		ids, err := roleIDsByName(byName, added)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("roles"), "Unknown OpenStack role", err.Error())
			return
		}
		if err := grantProjectRoles(ctx, r.config, domainID, userID, projectID, ids); err != nil {
			resp.Diagnostics.AddError("Failed to grant OpenStack project access", err.Error())
			return
		}
	}
	for _, name := range removed {
		id, ok := byName[name]
		if !ok {
			// A role that no longer exists in the domain cannot be held either.
			continue
		}
		if err := revokeProjectRole(ctx, r.config, domainID, userID, projectID, name, id); err != nil {
			resp.Diagnostics.AddError("Failed to revoke OpenStack project access", err.Error())
			return
		}
	}
	if len(removed) > 0 {
		waitRolesRevoked(ctx, r.config, domainID, userID, projectID, removed)
	}

	plan.ID, plan.DomainID = state.ID, state.DomainID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *openstackRoleAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !require(r.config, &resp.Diagnostics, false) {
		return
	}

	var state openstackRoleAssignmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	domainID, err := resolveDomainID(ctx, r.config, state.DomainID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("domain_id"), "Could not determine the OpenStack domain", err.Error())
		return
	}
	var names []string
	resp.Diagnostics.Append(state.Roles.ElementsAs(ctx, &names, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	byName, err := r.config.domainRoles(ctx, domainID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list OpenStack roles", err.Error())
		return
	}
	userID, projectID := state.UserID.ValueString(), state.ProjectID.ValueString()
	for _, name := range names {
		id, ok := byName[name]
		if !ok {
			continue
		}
		if err := revokeProjectRole(ctx, r.config, domainID, userID, projectID, name, id); err != nil {
			resp.Diagnostics.AddError("Failed to revoke OpenStack project access", err.Error())
			return
		}
	}
	waitRolesRevoked(ctx, r.config, domainID, userID, projectID, names)
}

// parseRoleAssignmentImportID accepts "<user_id>/<project_id>" or
// "<domain_id>/<user_id>/<project_id>"; domain is empty for the short form.
func parseRoleAssignmentImportID(id string) (domainID, userID, projectID string, err error) {
	format := fmt.Errorf("expected <user_id>/<project_id> or <domain_id>/<user_id>/<project_id>, got %q", id)
	parts := strings.Split(strings.TrimSpace(id), "/")
	switch len(parts) {
	case 2:
		userID, projectID = parts[0], parts[1]
	case 3:
		domainID, userID, projectID = parts[0], parts[1], parts[2]
	default:
		return "", "", "", format
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return "", "", "", fmt.Errorf("%w: every segment must be non-empty", format)
		}
	}
	return domainID, userID, projectID, nil
}

// ImportState accepts "<user_id>/<project_id>" or "<domain_id>/<user_id>/<project_id>".
func (r *openstackRoleAssignmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	domainID, userID, projectID, err := parseRoleAssignmentImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Unexpected Import Identifier", capitalizeFirst(err.Error())+".")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), userID+"/"+projectID)...)
	if domainID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("domain_id"), domainID)...)
	}
}

// capitalizeFirst upper-cases the first letter, so a lower-case error string
// reads as a sentence in a diagnostic.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// roleIDs resolves role names in a domain.
func (r *openstackRoleAssignmentResource) roleIDs(ctx context.Context, domainID string, names []string) ([]string, error) {
	byName, err := r.config.domainRoles(ctx, domainID)
	if err != nil {
		return nil, err
	}
	return roleIDsByName(byName, names)
}

// setDiff returns the elements only in wanted (added) and only in current (removed).
func setDiff(wanted, current []string) (added, removed []string) {
	in := func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	}
	for _, w := range wanted {
		if !in(current, w) {
			added = append(added, w)
		}
	}
	for _, c := range current {
		if !in(wanted, c) {
			removed = append(removed, c)
		}
	}
	return added, removed
}
