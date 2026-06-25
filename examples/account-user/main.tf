terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
  # Write-only arguments (password) require Terraform 1.11+.
  required_version = ">= 1.11.0"
}

provider "cleura" {
  cloud = "public"
}

variable "account_user_password" {
  type        = string
  sensitive   = true
  default     = "Change-Me-Please-1"
  description = "Write-only password (placeholder default; override per environment, never commit a real value)."
}

# A Cleura Cloud Management System account user, including its privileges.
# Privileges are a field of the user object in the Cleura API, so they are set
# inline here (the API has no separate role-assignment endpoint).
resource "cleura_account_user" "example" {
  username  = "jdoe"
  email     = "jane.doe@example.org"
  firstname = "Jane"
  lastname  = "Doe"

  # Write-only: sent to the API, never stored in state. Bump password_wo_version to rotate.
  password            = var.account_user_password
  password_wo_version = "1"

  # Roles are assigned through the privileges matrix. Each category grants an
  # access level: "full", "read", or "project".
  #
  # `meta` is required by the Cleura API but undocumented (always an empty string
  # in practice), so the provider defaults it to "" — just omit it. See
  # .agent/cleura-api-wishlist.md item 11.
  privileges = {
    monitoring = {
      type = "read"
    }
    users = {
      type = "full"
    }
    openstack = {
      type = "project"
      project_privileges = [
        {
          domain_id  = "0123456789abcdef0123456789abcdef"
          project_id = "fedcba9876543210fedcba9876543210"
          type       = "full"
        },
      ]
    }
  }
}

output "account_user_id" {
  value = cleura_account_user.example.id
}
