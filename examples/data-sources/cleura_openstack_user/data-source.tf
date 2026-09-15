# Fetch an OpenStack user by name (scoped to the domain serving the provider's
# region) ...
data "cleura_openstack_user" "by_name" {
  name = "ci-deployer"
}

# ... or by ID.
data "cleura_openstack_user" "by_id" {
  id = "6f1c0e6f2b1d4c0aa1b2c3d4e5f60718"
}

output "ci_user_enabled" {
  value = data.cleura_openstack_user.by_name.enabled
}
