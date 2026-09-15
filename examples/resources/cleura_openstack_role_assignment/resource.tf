terraform {
  # The user's password is a write-only argument, which needs Terraform 1.11 or later.
  required_version = ">= 1.11.0"
}

variable "ci_password" {
  type        = string
  sensitive   = true
  description = "Password for the CI OpenStack user. Never commit a real value."
}

resource "cleura_openstack_project" "example" {
  name        = "team-sandbox"
  description = "Sandbox for the platform team"
}

resource "cleura_openstack_user" "ci" {
  name                = "ci-deployer"
  description         = "Deploys from the CI pipeline"
  password            = var.ci_password
  password_wo_version = "1"
}

# Give an OpenStack user roles on a project. The resource owns all of the
# user's roles on that project: add or remove names from `roles` to change them.
resource "cleura_openstack_role_assignment" "ci_sandbox" {
  user_id    = cleura_openstack_user.ci.id
  project_id = cleura_openstack_project.example.id

  # Role names as listed by `cleura openstack role list`; "member" grants the
  # usual project access, "load-balancer_member" adds Octavia, "swiftoperator"
  # adds object storage.
  roles = ["member", "load-balancer_member"]
}
