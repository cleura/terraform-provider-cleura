# Changelog

## Unreleased

### Added

- **`expires_at` on `cleura_gardener_shoot_kubeconfig`.** The API now returns the
  expiry it granted, so rotation is driven by that instead of an estimate. The
  provider used to stamp the local mint time into `last_applied` and add the
  *requested* `expiration_seconds`, which diverged whenever the API clamped the
  lifetime.

### Changed

- Upgraded to `cleura-client-go` v0.3.0.
- **Worker taint values may be omitted.** A taint with only a key and an effect
  is valid in Kubernetes, and a taint with no value now reads back as null
  rather than an empty string.

### Deprecated

- **`last_applied` on `cleura_gardener_shoot_kubeconfig`**, superseded by
  `expires_at`. It is still written, and resources whose state predates
  `expires_at` keep using it to estimate expiry, so upgrading does not rotate
  any existing kubeconfig.

### Known issues

- **`image_name` on worker machines is effectively read-only.** The v0.3.0 API
  write schema carries only the image version, while reads still return the
  image name, so a configured `image_name` is not sent. The public cloud profile
  offers a single image (`gardenlinux`), so no image is currently unreachable.

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
