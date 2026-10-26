// Package provider exposes the Cleura provider to other Go modules, such as
// the Crossplane provider generated with Upjet, which serve it in-process
// instead of running the provider binary. The implementation stays in
// internal/provider; only what such consumers need is exported here.
package provider

import (
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"

	internalprovider "github.com/cleura/terraform-provider-cleura/internal/provider"
)

// New returns a constructor for the Cleura provider, as served by main.
func New(version string) func() fwprovider.Provider {
	return internalprovider.New(version)
}
