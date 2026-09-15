# Fetch an OpenStack project by name (scoped to the domain serving the
# provider's region) ...
data "cleura_openstack_project" "by_name" {
  name = "team-sandbox"
}

# ... or by ID.
data "cleura_openstack_project" "by_id" {
  id = "8a22c50af68e45c6b4dd7722cce8f93a"
}

output "sandbox_enabled" {
  value = data.cleura_openstack_project.by_name.enabled
}
