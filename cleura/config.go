package cleura

import (
	"fmt"
	"strings"
)

// ProviderConfig holds provider-scoped settings passed to resources and data sources.
type ProviderConfig struct {
	Client    *Client
	Cloud     string
	Region    string
	ProjectID string
}

// DefaultAPIURL returns the predefined REST API base URL for public and compliant cloud.
// Private clouds (any other cloud name) have no default and require an explicit url.
func DefaultAPIURL(cloud string) (string, error) {
	cloud = strings.TrimSpace(cloud)
	if cloud == "" {
		return "", fmt.Errorf("cloud must not be empty")
	}

	switch cloud {
	case "public":
		return "https://rest.cleura.cloud", nil
	case "compliant":
		return "https://rest.compliant.cleura.cloud", nil
	default:
		return "", fmt.Errorf("cloud %q has no default API URL; set url on the provider or use CLEURA_API_URL", cloud)
	}
}
