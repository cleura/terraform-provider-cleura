terraform {
  # The password is a write-only argument, which needs Terraform 1.11 or later.
  required_version = ">= 1.11.0"
}

variable "ci_password" {
  type        = string
  sensitive   = true
  description = "Password for the CI OpenStack user. Never commit a real value."
}

# An OpenStack (Keystone) user for a CI pipeline. It authenticates against
# OpenStack itself, for example through the OpenStack CLI or provider, and is
# distinct from a Cleura account user.
resource "cleura_openstack_user" "ci" {
  name        = "ci-deployer"
  description = "Deploys from the CI pipeline"

  # Write-only: sent to the API, never stored in state. Bump the version to
  # re-send a changed password on the next apply.
  password            = var.ci_password
  password_wo_version = "1"
}

# The user starts without access to any project; grant roles with a
# cleura_openstack_role_assignment resource.

output "openstack_user_id" {
  value = cleura_openstack_user.ci.id
}
