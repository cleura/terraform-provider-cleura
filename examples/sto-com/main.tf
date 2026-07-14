terraform {
  required_providers {
    cleura = {
      source  = "cleura/cleura"
      version = "~> 0.2"
    }
  }
}

variable "project_id" {
  type        = string
  description = "OpenStack project ID. Look up with the cleura_project data source in a bootstrap step, or from the Cleura console."
}

provider "cleura" {
  cloud      = "compliant"
  region     = "sto-com"
  project_id = var.project_id
}

# Optional: verify project_id matches a project name in the provider region.
data "cleura_project" "example" {
  name = "some-project"
}

resource "cleura_gardener_shoot" "example" {
  name               = "multi-az"
  kubernetes_version = "1.34.3"
  allowed_cidrs = [
    "192.168.0.0/16",
    "10.20.30.0/24"
  ]

  shoot_provider = {
    infrastructure_config = {
      floating_pool_name = "ext-net"
    }
    load_balancer_provider = "amphora"
    workers = [
      {
        name = "wg1"
        machine = {
          image_name    = "gardenlinux"
          image_version = "1877.13.0"
          type          = "b.2c4gb"
        }
        minimum     = 3
        maximum     = 3
        volume_size = "50Gi"
        zones = [
          "az1",
          "az2",
          "az3",
        ]
      },
    ]
  }
}

resource "cleura_gardener_shoot_kubeconfig" "example" {
  expiration_seconds = 3600
  shoot_name         = cleura_gardener_shoot.example.name
}

output "admin_kubeconfig" {
  value     = cleura_gardener_shoot_kubeconfig.example.kubeconfig
  sensitive = true
}

output "resolved_project_id" {
  value       = data.cleura_project.example.id
  description = "Project ID from name lookup; should match var.project_id."
}
