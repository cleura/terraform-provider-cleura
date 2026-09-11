package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Plumbing shared by the OpenStack identity resources and data sources
// (cleura_openstack_project, cleura_openstack_user): domain resolution,
// list-based lookups (the API has no single-item GET for projects or users),
// and API error rendering.

// errNotFound marks an HTTP 404 from the API so callers can treat "gone"
// distinctly from other failures.
var errNotFound = errors.New("not found")

// apiErrorDetail renders a non-2xx API response for a diagnostic: the message
// from the API's {"error": {"code", "message"}} envelope when present, else
// the raw body.
func apiErrorDetail(status int, body []byte) string {
	return apiErrorDetailRedacting(status, body, "")
}

// apiErrorDetailRedacting renders an API error for a request that carried a
// write-only secret. The documented error envelope is rendered as usual with
// the secret stripped from the message; anything else is withheld, because a
// body we cannot parse is a body whose echoed fields we cannot find.
func apiErrorDetailRedacting(status int, body []byte, secret string) string {
	var envelope api.FrameworkHttpErrorResponse
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		detail := fmt.Sprintf("%s (HTTP %d)", envelope.Error.Message, status)
		// The envelope's per-field errors say which property was rejected and
		// why. Without them a validation failure reads only as "One or more
		// properties are invalid", leaving the user to guess.
		if envelope.Error.Errors != nil {
			for _, e := range *envelope.Error.Errors {
				detail += fmt.Sprintf("\n  %s: %s", e.Key, e.Value)
			}
		}
		return redactSecret(detail, secret)
	}
	if b := strings.TrimSpace(string(body)); b != "" {
		if secret != "" {
			return fmt.Sprintf("HTTP %d (the API's response was not the documented error "+
				"envelope, so it has been withheld: the request carried a write-only password "+
				"and an unrecognised body may echo it)", status)
		}
		return fmt.Sprintf("HTTP %d: %s", status, b)
	}
	return fmt.Sprintf("HTTP %d", status)
}

// redactSecret removes a write-only secret from text that is on its way to a
// diagnostic. API errors are surfaced with their response body attached, and
// some APIs echo the rejected request fields back; a password that Terraform
// keeps out of state must not reach the console or a CI log either.
//
// The secret is matched in every encoding it might have picked up on the way
// back: a JSON body escapes it before we ever see it, so a password holding
// "&", "<", ">", a quote or a backslash does not appear literally. Go escapes
// the first three as \u0026, \u003c and \u003e; encoders elsewhere escape
// every non-ASCII rune the same way. Matching text alone can never be
// exhaustive, which is why apiErrorDetailRedacting withholds a body it cannot
// parse rather than trusting this to catch everything.
//
// The length guard keeps a short or empty value from matching unrelated text.
func redactSecret(text, secret string) string {
	if len(secret) < 8 {
		return text
	}
	for _, encoded := range secretEncodings(secret) {
		text = strings.ReplaceAll(text, encoded, "(redacted)")
	}
	return text
}

// secretEncodings returns the forms a secret can take in an API response body.
func secretEncodings(secret string) []string {
	encodings := []string{secret}

	// Go's encoder, which HTML-escapes & < > on top of the JSON basics.
	if b, err := json.Marshal(secret); err == nil && len(b) >= 2 {
		encodings = append(encodings, string(b[1:len(b)-1]))
	}
	// The same without HTML escaping, which is what many encoders emit.
	var plain strings.Builder
	enc := json.NewEncoder(&plain)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(secret); err == nil {
		if q := strings.TrimRight(plain.String(), "\n"); len(q) >= 2 {
			encodings = append(encodings, q[1:len(q)-1])
		}
	}
	// An ASCII-only encoder (Python's json.dumps default, for one) escapes
	// every non-ASCII rune as \uXXXX.
	var ascii strings.Builder
	for _, r := range secret {
		switch {
		case r > 127:
			fmt.Fprintf(&ascii, "\\u%04x", r)
		default:
			ascii.WriteRune(r)
		}
	}
	encodings = append(encodings, ascii.String())
	// Percent-encoding, in case the value was reflected through a URL.
	encodings = append(encodings, url.QueryEscape(secret))

	// Longest first, so a broader encoding is replaced before a narrower one
	// can leave a fragment behind, and without duplicates.
	slices.SortFunc(encodings, func(a, b string) int { return len(b) - len(a) })
	return slices.Compact(encodings)
}

// decodeJSON drains a response and decodes a 2xx body into out (nil to
// discard). A non-2xx status is returned as an error carrying the API's
// message; a 404 wraps errNotFound.
func decodeJSON(resp *http.Response, out any) error {
	return decodeJSONRedacting(resp, out, "")
}

// decodeJSONRedacting is decodeJSON for a request that carried a write-only
// secret, so an error body cannot carry it back out into a diagnostic.
func decodeJSONRedacting(resp *http.Response, out any, secret string) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", errNotFound, apiErrorDetailRedacting(resp.StatusCode, body, secret))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(apiErrorDetailRedacting(resp.StatusCode, body, secret))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// optionalStringValue maps an API string that may be absent or empty to a
// Terraform value: null for both, since the API reports a cleared field either
// way and the schema rejects an explicit "".
func optionalStringValue(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// ----- domains -----

// listDomains fetches the account's OpenStack domains.
func listDomains(ctx context.Context, cfg *ProviderConfig) ([]api.CommonOpenStackDomain, error) {
	resp, err := cfg.Client.OpenStackIdentityListDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing OpenStack domains: %w", err)
	}
	var domains []api.CommonOpenStackDomain
	if err := decodeJSON(resp, &domains); err != nil {
		return nil, fmt.Errorf("listing OpenStack domains: %w", err)
	}
	return domains, nil
}

// domainForRegion picks the domain that serves region from the account's
// domains.
//
// API WORKAROUND: every identity call is addressed to a domain ID and the API
// has no region-addressed lookup, so the domain is inferred from the provider's
// region (accounts have one domain per area). The sole domain is used when the
// account has only one; anything else needs an explicit domain_id.
func domainForRegion(domains []api.CommonOpenStackDomain, region string) (string, error) {
	var matches []api.CommonOpenStackDomain
	for _, d := range domains {
		for _, r := range d.Area.Regions {
			if strings.EqualFold(r.Tag, region) {
				matches = append(matches, d)
				break
			}
		}
	}
	switch {
	case len(matches) == 1:
		return matches[0].Id, nil
	case len(domains) == 0:
		return "", errors.New("the account has no OpenStack domains")
	case len(matches) == 0 && len(domains) == 1:
		return domains[0].Id, nil
	case len(matches) == 0:
		return "", fmt.Errorf("none of the account's %d OpenStack domains serves region %q; set domain_id explicitly. Available domains: %s",
			len(domains), region, describeDomains(domains))
	default:
		return "", fmt.Errorf("%d OpenStack domains serve region %q; set domain_id explicitly to choose one: %s",
			len(matches), region, describeDomains(matches))
	}
}

// describeDomains lists domains as "id (name, area)" for diagnostics.
func describeDomains(domains []api.CommonOpenStackDomain) string {
	parts := make([]string, 0, len(domains))
	for _, d := range domains {
		label := d.Id
		var extra []string
		if d.Name != nil && *d.Name != "" {
			extra = append(extra, *d.Name)
		}
		if d.Area.Name != "" {
			extra = append(extra, d.Area.Name)
		}
		if len(extra) > 0 {
			label += " (" + strings.Join(extra, ", ") + ")"
		}
		parts = append(parts, label)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// domainRegionTag returns a region tag the given domain serves, for the
// region-scoped endpoints that must be addressed within a domain.
//
// It deliberately fails rather than guessing. Its caller, projectExists, reads
// a 404 as "this project is gone" and its caller in turn drops the resource
// from state — after which the next apply creates a second project that can
// never be deleted. Guessing the provider's region for a domain that does not
// serve it produces exactly that 404, so an unresolvable domain has to surface
// as an error and leave the state alone.
func domainRegionTag(ctx context.Context, cfg *ProviderConfig, domainID string) (string, error) {
	domains, err := listDomains(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("listing OpenStack domains to find a region for domain %s: %w", domainID, err)
	}
	for _, d := range domains {
		if d.Id != domainID {
			continue
		}
		// Prefer the provider's region when this domain serves it, so the probe
		// stays on the region the rest of the run uses.
		for _, r := range d.Area.Regions {
			if strings.EqualFold(r.Tag, cfg.Region) {
				return r.Tag, nil
			}
		}
		if len(d.Area.Regions) > 0 {
			return d.Area.Regions[0].Tag, nil
		}
		return "", fmt.Errorf("OpenStack domain %s serves no regions", domainID)
	}
	return "", fmt.Errorf("OpenStack domain %s was not found in the account's domains", domainID)
}

// resolveDomainID returns the OpenStack domain to operate in: the explicit
// domain_id when set, else the domain serving the provider's region (resolved
// once per provider configuration and cached).
func resolveDomainID(ctx context.Context, cfg *ProviderConfig, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return cfg.regionDomainID(ctx)
}

// ----- projects -----

// listProjects fetches the caller's projects.
//
// API WORKAROUND: there is no per-domain project list and no single-project
// GET, so every project read lists the caller's projects and filters
// client-side. The listing covers only what the authenticated account user can
// access, and repeats a project under every region it is available in, so the
// result is flattened and deduplicated by id.
func listProjects(ctx context.Context, cfg *ProviderConfig) ([]api.OpenStackIdentityProject, error) {
	resp, err := cfg.Client.OpenStackIdentityListRegionsWithProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing OpenStack projects: %w", err)
	}
	var regions []api.OpenStackIdentityRegionWithProjects
	if err := decodeJSON(resp, &regions); err != nil {
		return nil, fmt.Errorf("listing OpenStack projects: %w", err)
	}
	seen := make(map[string]struct{})
	var out []api.OpenStackIdentityProject
	for _, r := range regions {
		for _, p := range r.Projects {
			if _, dup := seen[p.Id]; dup {
				continue
			}
			seen[p.Id] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}

// findProjectByID returns the listed project with the given id.
func findProjectByID(projects []api.OpenStackIdentityProject, id string) (*api.OpenStackIdentityProject, bool) {
	for i := range projects {
		if projects[i].Id == id {
			return &projects[i], true
		}
	}
	return nil, false
}

// findProjectByName returns the project named name within domainID (any domain
// when empty). Names are only unique within a domain, so more than one match
// is an error that asks for domain_id or an id lookup.
func findProjectByName(projects []api.OpenStackIdentityProject, domainID, name string) (*api.OpenStackIdentityProject, error) {
	var matches []api.OpenStackIdentityProject
	for _, p := range projects {
		if p.Name == name && (domainID == "" || p.DomainId == domainID) {
			matches = append(matches, p)
		}
	}
	scope := ""
	if domainID != "" {
		scope = fmt.Sprintf(" in OpenStack domain %s", domainID)
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no OpenStack project named %q%s is visible to the authenticated user", name, scope)
	case 1:
		return &matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, p := range matches {
			ids = append(ids, p.Id)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("%d OpenStack projects named %q%s; look the project up by id instead (%s)",
			len(matches), name, scope, strings.Join(ids, ", "))
	}
}

// projectExists probes a project the listing did not return.
//
// API WORKAROUND: with no single-project GET, the quota endpoint is the only
// per-project call that answers 404 for an unknown id (the per-project user
// list returns 200 [] for any id, PATCH returns 500). A 2xx means the project
// exists but is not visible in the caller's listing.
func projectExists(ctx context.Context, cfg *ProviderConfig, domainID, id string) (bool, error) {
	// The quota endpoint is region-scoped, and the region must be one the
	// project's own domain serves — with an explicit domain_id that is not
	// necessarily the provider's region. Probing with a foreign region tag
	// would answer 404 and the caller would wrongly drop a live project from
	// state, so Terraform would create a second one (irreversibly).
	region, err := domainRegionTag(ctx, cfg, domainID)
	if err != nil {
		return false, fmt.Errorf("checking OpenStack project %s: %w", id, err)
	}
	resp, err := cfg.Client.OpenStackIdentityGetProjectQuota(ctx, domainID, id, region)
	if err != nil {
		return false, fmt.Errorf("checking OpenStack project %s: %w", id, err)
	}
	err = decodeJSON(resp, nil)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("checking OpenStack project %s: %w", id, err)
	}
}

// ----- users -----

// listUsers fetches the OpenStack users of a domain.
//
// API WORKAROUND: there is no single-user GET, so every user read lists the
// domain and filters client-side.
func listUsers(ctx context.Context, cfg *ProviderConfig, domainID string) ([]api.OpenStackIdentityUser, error) {
	resp, err := cfg.Client.OpenStackIdentityListUsers(ctx, domainID)
	if err != nil {
		return nil, fmt.Errorf("listing OpenStack users in domain %s: %w", domainID, err)
	}
	var users []api.OpenStackIdentityUser
	if err := decodeJSON(resp, &users); err != nil {
		return nil, fmt.Errorf("listing OpenStack users in domain %s: %w", domainID, err)
	}
	return users, nil
}

// findUserByID returns the listed user with the given id.
func findUserByID(users []api.OpenStackIdentityUser, id string) (*api.OpenStackIdentityUser, bool) {
	for i := range users {
		if users[i].Id == id {
			return &users[i], true
		}
	}
	return nil, false
}

// findUserByName returns the listed user with the given (domain-unique) name.
func findUserByName(users []api.OpenStackIdentityUser, name string) (*api.OpenStackIdentityUser, bool) {
	for i := range users {
		if users[i].Name == name {
			return &users[i], true
		}
	}
	return nil, false
}

// userFromAccessView narrows the create/edit response type (which also carries
// project memberships) to the plain user shape returned by the list endpoint,
// so one state mapping serves every path.
func userFromAccessView(u *api.OpenStackIdentityUserWithProjectsAccess) *api.OpenStackIdentityUser {
	return &api.OpenStackIdentityUser{
		DefaultProjectId: u.DefaultProjectId,
		Description:      u.Description,
		DomainId:         u.DomainId,
		Enabled:          u.Enabled,
		Id:               u.Id,
		Name:             u.Name,
	}
}

// ----- roles and project access -----

// listRoles fetches the assignable roles of a domain as a name -> id map.
func listRoles(ctx context.Context, cfg *ProviderConfig, domainID string) (map[string]string, error) {
	resp, err := cfg.Client.OpenStackIdentityListRoles(ctx, domainID)
	if err != nil {
		return nil, fmt.Errorf("listing OpenStack roles in domain %s: %w", domainID, err)
	}
	var roles []api.OpenStackIdentityProjectRole
	if err := decodeJSON(resp, &roles); err != nil {
		return nil, fmt.Errorf("listing OpenStack roles in domain %s: %w", domainID, err)
	}
	byName := make(map[string]string, len(roles))
	for _, r := range roles {
		byName[r.Name] = r.Id
	}
	return byName, nil
}

// roleIDsByName maps role names to IDs (the grant/revoke endpoints take IDs),
// erroring on an unknown name with the available names listed.
func roleIDsByName(byName map[string]string, names []string) ([]string, error) {
	ids := make([]string, 0, len(names))
	for _, n := range names {
		id, ok := byName[n]
		if !ok {
			available := make([]string, 0, len(byName))
			for name := range byName {
				available = append(available, name)
			}
			sort.Strings(available)
			return nil, fmt.Errorf("unknown OpenStack role %q; available roles: %s", n, strings.Join(available, ", "))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// userProjectRoles returns the names of the roles userID holds on projectID,
// sorted; nil when the user has no access to the project (or no longer
// exists: the API answers an empty list for an unknown user).
func userProjectRoles(ctx context.Context, cfg *ProviderConfig, domainID, userID, projectID string) ([]string, error) {
	resp, err := cfg.Client.OpenStackIdentityListUserProjects(ctx, domainID, userID)
	if err != nil {
		return nil, fmt.Errorf("listing project access of OpenStack user %s: %w", userID, err)
	}
	var memberships []api.OpenStackIdentityProjectMembership
	if err := decodeJSON(resp, &memberships); err != nil {
		return nil, fmt.Errorf("listing project access of OpenStack user %s: %w", userID, err)
	}
	for _, m := range memberships {
		if m.Id != projectID || m.Roles == nil {
			continue
		}
		names := make([]string, 0, len(*m.Roles))
		for _, r := range *m.Roles {
			names = append(names, r.Name)
		}
		sort.Strings(names)
		return names, nil
	}
	return nil, nil
}

// grantProjectRoles gives userID the roles (IDs) on projectID. Verified live:
// additive and idempotent.
func grantProjectRoles(ctx context.Context, cfg *ProviderConfig, domainID, userID, projectID string, roleIDs []string) error {
	body := api.OpenStackIdentityGrantProjectAccessRequest{
		Projects: []api.OpenStackIdentityProjectAccessRequest{{ProjectId: projectID, Roles: roleIDs}},
	}
	resp, err := cfg.Client.OpenStackIdentityGrantProjectAccess(ctx, domainID, userID, body)
	if err != nil {
		return fmt.Errorf("granting project access: %w", err)
	}
	if err := decodeJSON(resp, nil); err != nil {
		return fmt.Errorf("granting project access: %w", err)
	}
	return nil
}

// revokeProjectRole removes one role from userID on projectID.
//
// API WORKAROUND: revoking a role that is not assigned fails with an opaque 500
// instead of a 404 or an idempotent 204, so on any error the current
// assignment is re-read and the revoke counts as done when the role is gone.
func revokeProjectRole(ctx context.Context, cfg *ProviderConfig, domainID, userID, projectID, roleName, roleID string) error {
	resp, err := cfg.Client.OpenStackIdentityRevokeProjectAccess(ctx, domainID, userID, projectID, roleID)
	if err != nil {
		return fmt.Errorf("revoking role %q: %w", roleName, err)
	}
	if err := decodeJSON(resp, nil); err != nil {
		// The same 500 means "already revoked", so check the assignment before
		// failing an apply. One read is not enough: the listing alternates
		// between replicas, so a role that is gone can still read as held.
		for attempt := 0; attempt < revokeConfirmReads; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
			held, listErr := userProjectRoles(ctx, cfg, domainID, userID, projectID)
			if listErr == nil && !slices.Contains(held, roleName) {
				return nil
			}
		}
		return fmt.Errorf("revoking role %q: %w (the API also fails this way when the role was already revoked "+
			"but its listing is stale; retry in a few minutes)", roleName, err)
	}
	return nil
}

// revokeConfirmReads is how many times revokeProjectRole re-reads the
// assignment after an error before giving up.
const revokeConfirmReads = 4

// revokeSettleTimeout and revokeSettleReads bound waitRolesRevoked.
//
// API WORKAROUND: after a revoke the user's project listing alternates between
// the old and the new answer for minutes (several backend nodes, each with its
// own cache). A single fresh read proves little, so the loop wants a few
// consecutive fresh reads and gives up after the timeout rather than hang.
const (
	revokeSettleTimeout = 60 * time.Second
	revokeSettleReads   = 3
)

// waitRolesRevoked polls until revokeSettleReads consecutive listings no longer
// show any of the revoked role names for the user on the project, or the
// timeout passes (then it gives up silently: the next refresh shows the truth).
func waitRolesRevoked(ctx context.Context, cfg *ProviderConfig, domainID, userID, projectID string, revoked []string) {
	deadline := time.Now().Add(revokeSettleTimeout)
	fresh := 0
	for {
		held, err := userProjectRoles(ctx, cfg, domainID, userID, projectID)
		if err != nil {
			return
		}
		stale := false
		for _, r := range revoked {
			if slices.Contains(held, r) {
				stale = true
				break
			}
		}
		if stale {
			fresh = 0
		} else {
			fresh++
			if fresh >= revokeSettleReads {
				return
			}
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
