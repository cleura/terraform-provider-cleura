package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// ensureBootstrapped prepares the provider's project for Gardener. A project
// must be bootstrapped before a shoot can be created in it, and the API offers
// no way to ask whether it already is: GET and DELETE on the bootstrap path
// both answer 404, so there is nothing to read and nothing to undo.
//
// API WORKAROUND: with no status endpoint the only way to guarantee the
// project is ready is to bootstrap it every time a shoot is created. Measured
// live on 2026-09-15, a repeat POST on an already-bootstrapped project answers
// the same 204 in roughly a quarter of the time of the first call, so it
// short-circuits rather than re-provisioning. That idempotency is observed,
// not promised by the API — see item 26 in
// .agent/cleura-api-wishlist-gardener.md, which asks for it to be documented
// along with a status GET that would make this call unnecessary.
func ensureBootstrapped(ctx context.Context, cfg *ProviderConfig) diag.Diagnostics {
	var diags diag.Diagnostics

	response, err := cfg.Client.GardenerCommunicationBootstrap(ctx, cfg.Cloud, cfg.Region, cfg.ProjectID)
	if err != nil {
		diags.AddError("Failed to prepare the project for Gardener", err.Error())
		return diags
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		diags.AddError("Failed to read response body", err.Error())
		return diags
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		diags.AddError(
			fmt.Sprintf("Failed to prepare project %s for Gardener (HTTP %d)", cfg.ProjectID, response.StatusCode),
			bootstrapErrorDetail(response.StatusCode, cfg.ProjectID, body))
	}
	return diags
}

// bootstrapErrorDetail turns the API's bootstrap failures into something a
// user can act on.
//
// API WORKAROUND: an unknown project id answers 500 "Internal Server Error"
// rather than a 404, so the status alone cannot be trusted to mean an outage;
// the likeliest cause is named here instead.
func bootstrapErrorDetail(status int, projectID string, body []byte) string {
	detail := apiErrorDetail(status, body)
	if status == http.StatusInternalServerError {
		return detail + fmt.Sprintf("\n\nThe API answers an unknown project ID with this error as well. Check that "+
			"project %q exists in the provider's region and that your account can reach it.", projectID)
	}
	return detail
}
