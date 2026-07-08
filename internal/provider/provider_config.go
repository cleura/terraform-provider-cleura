package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/cleura/cleura-client-go/cleura"
)

// ProviderConfig holds provider-scoped settings passed to resources and data
// sources. (Provider plumbing — deliberately not part of the shared client.)
type ProviderConfig struct {
	Client    *cleura.Client
	Cloud     string
	Region    string
	ProjectID string
}

func fromResource(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) *ProviderConfig {
	return fromProviderData(ctx, req.ProviderData, &resp.Diagnostics)
}

func fromDataSource(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *ProviderConfig {
	return fromProviderData(ctx, req.ProviderData, &resp.Diagnostics)
}

func fromProviderData(_ context.Context, providerData any, diags *diag.Diagnostics) *ProviderConfig {
	// ProviderData is nil during early framework validation passes; that is expected.
	if providerData == nil {
		return nil
	}

	cfg, ok := providerData.(*ProviderConfig)
	if !ok {
		diags.AddError(
			"Unexpected Provider Configure Type",
			fmt.Sprintf("Expected *ProviderConfig, got %T", providerData),
		)
		return nil
	}

	return cfg
}

func require(cfg *ProviderConfig, diags *diag.Diagnostics, requireProjectID bool) bool {
	if cfg == nil || cfg.Client == nil {
		diags.AddError(
			"Unconfigured Cleura Provider",
			"The Cleura provider failed to configure. Check for provider configuration errors above. "+
				"Ensure cloud, region, username, and token are set on the provider or via CLEURA_CLOUD, CLEURA_REGION, "+
				"CLEURA_API_USERNAME, and CLEURA_API_TOKEN. When debugging with TF_REATTACH_PROVIDERS, credentials must be "+
				"set in the debugger environment (e.g. .vscode/launch.json), not only in your shell.",
		)
		return false
	}

	if requireProjectID && cfg.ProjectID == "" {
		diags.AddError(
			"Missing Cleura project_id",
			"Set project_id on the provider configuration or use the CLEURA_PROJECT_ID environment variable. "+
				"It is required for Gardener resources.",
		)
		return false
	}

	return true
}
