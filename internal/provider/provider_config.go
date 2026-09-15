package provider

import (
	"context"
	"fmt"
	"sync"

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

	// OpenStack domain serving Region, resolved lazily by regionDomainID and
	// cached for the life of the provider configuration so that each OpenStack
	// identity resource does not re-list the account's domains on every call.
	domainMu sync.Mutex
	domainID string

	// Assignable OpenStack roles per domain (name -> id), cached the same way.
	rolesMu sync.Mutex
	roles   map[string]map[string]string
}

// domainRoles returns the assignable roles of an OpenStack domain as a
// name -> id map, fetched once per domain.
func (c *ProviderConfig) domainRoles(ctx context.Context, domainID string) (map[string]string, error) {
	c.rolesMu.Lock()
	defer c.rolesMu.Unlock()
	if byName, ok := c.roles[domainID]; ok {
		return byName, nil
	}
	byName, err := listRoles(ctx, c, domainID)
	if err != nil {
		return nil, err
	}
	if c.roles == nil {
		c.roles = map[string]map[string]string{}
	}
	c.roles[domainID] = byName
	return byName, nil
}

// regionDomainID resolves (once) the OpenStack domain that serves the
// provider's region. See domainForRegion for the selection rules.
func (c *ProviderConfig) regionDomainID(ctx context.Context) (string, error) {
	c.domainMu.Lock()
	defer c.domainMu.Unlock()
	if c.domainID != "" {
		return c.domainID, nil
	}
	domains, err := listDomains(ctx, c)
	if err != nil {
		return "", err
	}
	id, err := domainForRegion(domains, c.Region)
	if err != nil {
		return "", err
	}
	c.domainID = id
	return id, nil
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
