# STEP 1:
# Creates a shoot cluster with a minimal configuration, managing one worker group
# Tests the capability to determine "known after apply"

resource "cleura_gardener_shoot" "test" {
  name               = var.name
  kubernetes_version = var.kubernetes_version

  shoot_provider = {
    infrastructure_config = {
      floating_pool_name = var.floating_pool_name
    }
    load_balancer_provider = "amphora"
    workers = [
      {
        name = "wg1"
        machine = {
          image_name    = "gardenlinux"
          image_version = var.image_version
          type          = var.flavor_name
        }
        volume_size = "50Gi"
        zones       = var.wg1_zones
      },
    ]
  }
}

variable "image_version" {
  type = string
}

variable "kubernetes_version" {
  type = string
}

variable "floating_pool_name" {
  type    = string
  default = "ext-net"
}

variable "wg1_zones" {
  type    = list(string)
  default = null
}

variable "name" {
  type    = string
  default = "acctest"
}

variable "flavor_name" {
  type    = string
  default = "b.2c4gb"
}
