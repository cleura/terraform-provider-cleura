terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
    openstack = {
      source  = "terraform-provider-openstack/openstack"
      version = "3.4.0"
    }
  }
}

provider "cleura" {
  url = "https://rest.compliant.cleura.cloud"
}

provider "openstack" {}

data "openstack_identity_project_v3" "this" {
  name = "some-project"
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
  open_stack_region_tag = "sto-com"
  open_stack_project_id = data.openstack_identity_project_v3.this.id

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
