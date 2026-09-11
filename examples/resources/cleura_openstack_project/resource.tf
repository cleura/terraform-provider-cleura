# An OpenStack project in the provider's region. The project is created in the
# OpenStack domain that serves that region; set domain_id to pick another one.
resource "cleura_openstack_project" "example" {
  name        = "team-sandbox"
  description = "Sandbox for the platform team"

  # Optional; defaults to true. Destroying the resource sets this to false,
  # because the Cleura API cannot delete projects.
  enabled = true
}

output "project_id" {
  value = cleura_openstack_project.example.id
}
