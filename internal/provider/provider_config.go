package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	cleura "github.com/cleura/terraform-provider-cleura/client"
)

func providerConfigFromResource(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) *cleura.ProviderConfig {
	return providerConfig(ctx, req.ProviderData, &resp.Diagnostics, true)
}

func providerConfigFromDataSource(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *cleura.ProviderConfig {
	return providerConfig(ctx, req.ProviderData, &resp.Diagnostics, false)
}

func providerConfig(_ context.Context, providerData any, diags *diag.Diagnostics, requireProjectID bool) *cleura.ProviderConfig {
	// ProviderData is nil during early framework validation passes; that is expected.
	if providerData == nil {
		return nil
	}

	cfg, ok := providerData.(*cleura.ProviderConfig)
	if !ok {
		diags.AddError(
			"Unexpected Provider Configure Type",
			fmt.Sprintf("Expected *cleura.ProviderConfig, got %T", providerData),
		)
		return nil
	}

	return cfg
}

func requireProviderConfig(cfg *cleura.ProviderConfig, diags *diag.Diagnostics, requireProjectID bool) bool {
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
