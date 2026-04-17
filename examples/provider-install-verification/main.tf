terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

provider "cleura" {
  url = "https://rest.cleura.cloud"
}

resource "cleura_shoot" "example" {
  name               = "kekwait"
  kubernetes_version = "1.33.9"
  # allowed_cidrs = [
  #   "192.168.0.0/16",
  #   "10.20.30.0/24"
  # ]

  # openstack id and gardener region tag must be required
  open_stack_region_tag = "Kna1"
  open_stack_project_id = "f0546dd8e1c94376bde39086ff57fbd3"
  gardener_region_tag   = "public"
  shoot_provider = {
    infrastructure_config = {
      floating_pool_name = "ext-net"
    }
    load_balancer_provider = "amphora" # Default to amphora?
    workers = [
      {
        name = "ccc"
        machine = {
          image_name    = "gardenlinux"
          image_version = "1877.13.0"
          type          = "b.2c4gb"
        }
        minimum     = 1
        volume_size = "50Gi"
        # labels = [
        #   { key = "aaaa", value = "bbbb" }
        # ]
      },
    ]
  }
}
