# Prepare a project for Gardener. A project must be bootstrapped before any
# cleura_gardener_shoot can be created in it.

# The common case: bootstrap the project the provider is already pointed at.
resource "cleura_gardener_bootstrap" "default" {}

# Bootstrapping a project created in the same configuration. The provider's
# project_id has to be known before the run starts, so it cannot refer to a
# project this provider creates; pass the ID to the resource instead.
resource "cleura_openstack_project" "team" {
  name        = "team-sandbox"
  description = "Sandbox for the platform team"
}

resource "cleura_gardener_bootstrap" "team" {
  project_id = cleura_openstack_project.team.id
}

output "bootstrapped_project_id" {
  value = cleura_gardener_bootstrap.team.project_id
}
