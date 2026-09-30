# Fetch an OpenStack project by name (scoped to the domain serving the
# provider's region) ...
data "cleura_openstack_project" "by_name" {
  name = "team-sandbox"
}

# ... or by ID.
data "cleura_openstack_project" "by_id" {
  id = "0f3c1e2a4b5d6e7f8091a2b3c4d5e6f7"
}

output "sandbox_enabled" {
  value = data.cleura_openstack_project.by_name.enabled
}
