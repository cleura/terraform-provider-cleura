terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
  required_version = ">= 1.11.0" # write-only password
}

provider "cleura" {
  cloud = "public"
  # CLEURA_API_USERNAME / CLEURA_API_TOKEN are read from the environment.
}

# ---------------------------------------------------------------------------
# Inputs
# ---------------------------------------------------------------------------

# Password applied to every user in this example. Write-only: sent to the API on
# create/update, never stored in state. It has a placeholder default so the
# example runs as-is -- override it (TF_VAR_account_user_password, a *.tfvars
# file, or a secrets manager) and never commit a real value. For distinct
# per-user passwords, make this a map(string) and look it up with each.key.
variable "account_user_password" {
  type        = string
  sensitive   = true
  default     = "Change-Me-Please-1"
  description = "Write-only password applied to every account user in this example. Placeholder default; override per environment and never commit a real value."
}

# The team to manage, keyed by username (3-40 chars of [0-9a-z_.-]).
#
# Each user lists only the privilege categories they need; omitted categories
# grant no access. The optional(...) wrappers are what let every user declare a
# different subset of privileges without Terraform raising "inconsistent object
# types" across the map.
#
# NOTE: the API accepts invoice, monitoring, users, and openstack. It rejects
# `account` with HTTP 400 ("Unsupported privilege"); ai_gateway and application
# are untested. The schema exposes all of them, but stick to the confirmed set.
variable "users" {
  type = map(object({
    email     = string
    firstname = optional(string)
    lastname  = optional(string)
    privileges = optional(object({
      account     = optional(object({ type = string }))
      ai_gateway  = optional(object({ type = string }))
      application = optional(object({ type = string }))
      invoice     = optional(object({ type = string }))
      monitoring  = optional(object({ type = string }))
      users       = optional(object({ type = string }))
      openstack = optional(object({
        type = string
        project_privileges = optional(list(object({
          domain_id  = string
          project_id = string
          type       = string
        })))
      }))
    }))
  }))

  default = {
    # Full administrator.
    alice_example = {
      email     = "alice@example.org"
      firstname = "Alice"
      lastname  = "Admin"
      privileges = {
        users      = { type = "full" }
        invoice    = { type = "full" }
        monitoring = { type = "full" }
        openstack  = { type = "full" }
      }
    }
    # Billing only.
    bob_example = {
      email     = "bob@example.org"
      firstname = "Bob"
      lastname  = "Biller"
      privileges = {
        invoice = { type = "full" }
      }
    }
    # Read-only auditor across a few areas.
    carol_example = {
      email     = "carol@example.org"
      firstname = "Carol"
      lastname  = "Auditor"
      privileges = {
        invoice    = { type = "read" }
        monitoring = { type = "read" }
      }
    }
    # Ops with full access to one OpenStack project. NOTE: project-scoped access
    # requires a REAL, accessible domain_id/project_id -- the API rejects unknown
    # projects with HTTP 400. Replace the placeholders below with your own.
    dave_example = {
      email     = "dave@example.org"
      firstname = "Dave"
      lastname  = "Ops"
      privileges = {
        monitoring = { type = "full" }
        openstack = {
          type = "project"
          project_privileges = [{
            domain_id  = ""
            project_id = "fedcba9876543210fedcba9876543210"
            type       = "full"
          }]
        }
      }
    }
    # Second ops user with full access to the same project. NOTE: the API coerces
    # a project-level "read" to "full" (see wishlist item 16), so use "full" here.
    erin_example = {
      email     = "erin@example.org"
      firstname = "Erin"
      lastname  = "Ops"
      privileges = {
        openstack = {
          type = "project"
          project_privileges = [{
            domain_id  = "0123456789abcdef0123456789abcdef"
            project_id = "fedcba9876543210fedcba9876543210"
            type       = "full"
          }]
        }
      }
    }
  }
}

# ---------------------------------------------------------------------------
# Users
# ---------------------------------------------------------------------------

resource "cleura_account_user" "team" {
  for_each = var.users

  username  = each.key
  email     = each.value.email
  firstname = each.value.firstname
  lastname  = each.value.lastname

  # Write-only password (shared across the team in this example) + its version
  # companion. Bump password_wo_version to re-send (rotate) it on the next apply.
  password            = var.account_user_password
  password_wo_version = "1"

  # Omitted categories grant no access; removing a category later revokes it.
  privileges = each.value.privileges
}

output "account_user_ids" {
  value = { for name, u in cleura_account_user.team : name => u.id }
}
