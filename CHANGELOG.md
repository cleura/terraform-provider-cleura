# Changelog

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
