terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

provider "cleura" {
  url = "https://rest.compliant.cleura.cloud"
}

data "cleura_project" "example" {
  name                  = "some-project"
  open_stack_region_tag = "sto-com"
}

resource "cleura_shoot" "example" {
  name               = "multi-az"
  kubernetes_version = "1.34.3"
  allowed_cidrs = [
    "192.168.0.0/16",
    "10.20.30.0/24"
  ]

  // When using compliant cloud, gardener region tag is "compliant"
  gardener_region_tag   = "compliant"
  open_stack_project_id = data.cleura_project.example.id
  open_stack_region_tag = data.cleura_project.example.open_stack_region_tag

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

resource "cleura_shoot_kubeconfig" "example" {
  expiration_seconds = 3600 # One hour

  shoot_name            = cleura_shoot.example.name
  gardener_region_tag   = cleura_shoot.example.gardener_region_tag
  open_stack_region_tag = cleura_shoot.example.open_stack_region_tag
  open_stack_project_id = cleura_shoot.example.open_stack_project_id
}

output "admin_kubeconfig" {
  value     = cleura_shoot_kubeconfig.example.kubeconfig
  sensitive = true
}
