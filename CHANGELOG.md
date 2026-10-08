# Changelog

## v0.3.2

This release lets `cleura_openstack_user` work with tools that don't support
write-only arguments, such as Crossplane. The write-only password is renamed to
`password_wo`: see **Breaking changes** before upgrading.

### Breaking changes

- **`cleura_openstack_user`: the write-only password is now `password_wo`, and
  `password` is an ordinary argument stored in state.**
  - To keep the password out of state, rename `password` to `password_wo`, and
    keep `password_wo_version` alongside it.
  - A configuration that still sets `password` together with
    `password_wo_version` fails at plan with that hint.
  - A configuration that sets `password` without `password_wo_version` keeps
    working, but the next apply stores the password in state. The plan warns
    about it; rename `password` to `password_wo` to keep it out of state.

### Added

- **`cleura_openstack_user` works with tools that don't support write-only
  arguments**, such as Crossplane, and Terraform or OpenTofu before 1.11.
  - Set `password`, which is stored in state and marked sensitive.
  - Exactly one of `password` and `password_wo` must be set.

## v0.3.1

A documentation-only release; the provider behaves exactly as in v0.3.0.

### Changed

- **Clearer, shorter documentation.** Notes on resource pages now describe
  what to expect and what to do, without implementation detail: the
  kubeconfig, project, role assignment and shoot pages, and the
  authentication and OpenStack identity guides.

## v0.3.0

This release adds OpenStack project, user and role-assignment resources, lets a
Gardener cluster name its own project, and gives both Gardener resources an `id`.

### Added

- **`cleura_openstack_project` resource** to create and manage OpenStack (Keystone)
  projects: name, description, and enabled state, in the domain serving the
  provider's region (or an explicit `domain_id`). Projects in Cleura are
  permanent, so destroying the resource disables the project.
- **`cleura_openstack_user` resource** to create and manage OpenStack users with a
  write-only password (Terraform 1.11+), description, and enabled state.
- **`cleura_openstack_role_assignment` resource** to grant a user roles on a
  project by role name, managing all of the user's roles on that project.
- **`cleura_openstack_project` and `cleura_openstack_user` data sources** to fetch a
  project or user by ID or name.
- **`cleura_gardener_shoot` now prepares its project for Gardener itself.** A
  project must be bootstrapped before it can hold a shoot; creating a shoot now
  does that first, so a project created by `cleura_openstack_project` in the same
  configuration works with no extra step. The step runs on every create and has
  no effect on a project that is already prepared.
- **`expires_at` on `cleura_gardener_shoot_kubeconfig`**, the expiry Cleura
  granted. Rotation is now based on it instead of an estimate from the
  requested `expiration_seconds`, so it stays correct when the granted lifetime
  is shorter than requested.
- **`id` on `cleura_gardener_shoot` and `cleura_gardener_shoot_kubeconfig`.**
  Both are `<project_id>/<name>`; for a kubeconfig, that is the cluster it
  belongs to. Tools that identify resources by their `id` can now track them.
  Read-only; existing resources get it on the next refresh with no change
  planned.
- **`cleura_gardener_shoot` can be imported by its `id`.** Besides the bare shoot
  name, import now accepts `<project_id>/<name>`, which may name any project the
  credentials can access.
- **`project_id` on `cleura_gardener_shoot` and `cleura_gardener_shoot_kubeconfig`,**
  defaulting to the provider's. A provider's `project_id` has to be resolvable
  before the run starts, so it can never refer to a project the same
  configuration creates; setting it on the resource lets a project and a
  cluster inside it be created in one apply.

### Changed

- Write-only passwords are now removed from error messages, so a
  `cleura_openstack_user` password can never appear in console output or CI
  logs. It never reaches Terraform state either.
- Upgraded to `cleura-client-go` v0.3.0.
- Upgraded `google.golang.org/grpc` to v1.83.2, which fixes three advisories in
  earlier versions (two high, one medium).
- **Worker taints without a value.** A taint needs only a key and an effect.
  Such a taint reads back with `value = ""`; write `value = ""` to configure one.

### Fixed

- **Editing the provider's `project_id` no longer retargets existing clusters.**
  Every Gardener call read the provider's project at call time, including refresh,
  update and delete, so repointing the provider made Terraform look for a cluster
  in the wrong project — reading a 404 and planning a recreate, or editing worker
  groups against a project the cluster does not live in. The project a cluster was
  created in is now recorded in state and used for its whole lifetime. Clusters
  already in state adopt their current project on the next refresh, with no diff.

### Deprecated

- **`cleura_project` data source**, superseded by `cleura_openstack_project`,
  which does the same name lookup and additionally accepts an `id` and returns
  the project's `domain_id`, `description`, and `enabled` state. Migrating is a
  rename; the `name` argument and `id` attribute are unchanged. `cleura_project`
  keeps working and is not scheduled for removal in this major version.
- **`last_applied` on `cleura_gardener_shoot_kubeconfig`**, superseded by
  `expires_at`. It is still written, and resources whose state predates
  `expires_at` keep using it to estimate expiry, so upgrading does not rotate
  any existing kubeconfig.

### Known issues

- **`image_name` on worker machines is effectively read-only.** Only
  `image_version` is applied; the image name is read back from Cleura, so a
  configured `image_name` has no effect. The public cloud profile offers a single
  image (`gardenlinux`), so no image is currently unreachable.

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
