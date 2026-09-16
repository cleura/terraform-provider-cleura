# End-to-end starter: OpenStack project + user (member) + Gardener cluster.
#
# Prerequisites: `cleura login` (or set CLEURA_API_USERNAME / CLEURA_API_TOKEN).
# Gardener runs in the project created below. The provider has no `project_id`:
# provider configuration must resolve before the run starts, so it can never
# reference a project this configuration creates. The Gardener resources name the
# project themselves instead, which is what lets all of this be one apply.
#
# Usage:
#   terraform init
#   terraform apply -var='openstack_user_password=...'
#
terraform {
  required_version = ">= 1.11.0"

  required_providers {
    cleura = {
      source  = "cleura/cleura"
      version = "~> 0.2"
    }
  }
}

variable "cloud" {
  type        = string
  default     = "public"
  description = "Cleura cloud: public, compliant, or a private cloud name."
}

variable "region" {
  type        = string
  default     = "Sto2"
  description = "Region tag as reported by Cleura (case-sensitive, e.g. Sto2)."
}

variable "project_name" {
  type        = string
  default     = "platform-starter"
  description = "Name of the OpenStack project to create."
}

variable "openstack_user_name" {
  type        = string
  default     = "platform-operator"
  description = "OpenStack (Keystone) user created in the same region domain."
}

variable "openstack_user_password" {
  type        = string
  sensitive   = true
  description = "Password for the OpenStack user (write-only; never commit a real value)."
}

variable "cluster_name" {
  type        = string
  default     = "starter"
  description = "Gardener shoot name (must be unique within the project)."
}

provider "cleura" {
  cloud  = var.cloud
  region = var.region

  # No project_id here on purpose — see the note at the top. Each Gardener
  # resource below sets its own.
}

resource "cleura_openstack_project" "platform" {
  name        = var.project_name
  description = "Project for the starter Gardener cluster and OpenStack user"
}

resource "cleura_openstack_user" "operator" {
  name        = var.openstack_user_name
  description = "OpenStack user with member access to ${var.project_name}"

  password            = var.openstack_user_password
  password_wo_version = "1"
}

resource "cleura_openstack_role_assignment" "operator_member" {
  user_id    = cleura_openstack_user.operator.id
  project_id = cleura_openstack_project.platform.id
  roles      = ["member"]
}

resource "cleura_gardener_shoot" "starter" {
  # The project created above. Creating the shoot also prepares that project for
  # Gardener, which is irreversible — there is no teardown for it.
  project_id         = cleura_openstack_project.platform.id
  name               = var.cluster_name
  kubernetes_version = "1.35.6"

  shoot_provider = {
    load_balancer_provider = "amphora"

    infrastructure_config = {
      floating_pool_name = "ext-net"
    }

    workers = [
      {
        name = "default"
        machine = {
          #          image_name    = "gardenlinux"
          image_version = "1877.19.0"
          type          = "b.2c4gb"
        }
        minimum     = 2
        maximum     = 2
        volume_size = "50Gi"
        zones       = ["nova"]
      },
    ]
  }
}

resource "cleura_gardener_shoot_kubeconfig" "starter" {
  # Must match the shoot's project; nothing cross-checks it for you.
  project_id         = cleura_openstack_project.platform.id
  shoot_name         = cleura_gardener_shoot.starter.name
  expiration_seconds = 3600
}

output "project_id" {
  value       = cleura_openstack_project.platform.id
  description = "OpenStack project ID, which the Gardener resources above are pointed at."
}

output "openstack_user_id" {
  value = cleura_openstack_user.operator.id
}

output "cluster_name" {
  value = cleura_gardener_shoot.starter.name
}

output "kubeconfig" {
  value       = cleura_gardener_shoot_kubeconfig.starter.kubeconfig
  sensitive   = true
  description = "Short-lived admin kubeconfig for the shoot."
}
