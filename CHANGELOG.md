# Changelog

## v0.2.0

This release adds authentication through the cleura CLI, moves the provider onto
the shared `cleura-client-go` API client, and ships a complete documentation set.
It is backward compatible; existing configurations upgrade with no changes.

### Added

- **Authentication through the cleura CLI.** Run `cleura login` once and the
  provider reuses those credentials automatically, so a provider block needs only
  `cloud`, `region`, and `project_id`, and no API token has to live in your
  Terraform configuration or state. Credentials are resolved in order: the
  provider configuration, then the `CLEURA_API_*` environment variables, then the
  CLI.
- **`use_cli` and `profile` provider attributes** to control the CLI fallback:
  turn it off with `use_cli = false`, or read from a named `cleura login` profile.
- **Documentation on the Terraform Registry:** a Getting Started walkthrough, an
  Authentication and CI guide, and a full attribute reference for every resource
  and the `cleura_project` data source.

### Changed

- Rebuilt on the generated, shared `cleura-client-go` API client. Existing
  `username`/`token` configuration and `CLEURA_API_*` environment variables keep
  working and take precedence over the CLI.

### Fixed

- Clearer credential diagnostics: the provider warns when the username and token
  come from different sources, when the configured cloud or endpoint does not
  match the CLI credentials, and when a CLI token is likely expired.
- `cleura_gardener_shoot_kubeconfig` reports a missing `project_id` at plan time
  instead of failing during apply.
- A crashing or outdated cleura CLI now produces a clear message instead of a
  misleading "not logged in", and endpoint comparison ignores a trailing slash.

## v0.1.0

Initial public (experimental) release.

### Resources

- `cleura_gardener_shoot` — manage Gardener-based Kubernetes clusters: worker
  groups, maintenance windows, hibernation schedules, allowed login CIDRs, and
  Calico/Cilium networking (the `networking` block, including `cilium_provider_config`).
- `cleura_gardener_shoot_kubeconfig` — issue short-lived admin kubeconfigs, with
  automatic recreation on expiry. Changing `shoot_name` or `expiration_seconds`
  reissues the credential.

### Data sources

- `cleura_project` — look up Cleura projects.

### Notes

- This provider is in early development with limited testing and is not yet
  recommended for production use.
- Some enum fields render as plain strings in the registry docs (e.g.
  `networking.type`); the allowed values are still enforced by the provider.
  (Tracked for a spec-side docs improvement.)
