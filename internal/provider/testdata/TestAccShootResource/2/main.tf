terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

provider "cleura" {
  cloud      = var.cloud
  region     = var.region
  project_id = var.project_id
}

resource "cleura_gardener_shoot" "test" {
  name               = var.name
  kubernetes_version = var.kubernetes_version
  allowed_cidrs = [
    "192.168.0.0/16",
    "10.0.0.0/8",
  ]
  enable_ha_control_plane = true

  maintenance = {
    auto_update = {
      kubernetes_version    = false,
      machine_image_version = true,
    }
    time_window = {
      begin = "140000+0000"
      end   = "150000+0000"
    }
  }

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

        max_surge = 1
        maximum   = 2
        minimum   = 2

        annotations = [
          { key = "annotation1", value = "annotationvalue1" },
          { key = "annotation2", value = "annotationvalue2" },
        ]
        labels = [
          { key = "label1", value = "labelvalue1" },
        ]
        taints = [
          { key = "taint1", value = "taintvalue1", effect = "NoExecute" },
        ]
      },
      {
        name = "wg2"
        machine = {
          image_name    = "gardenlinux"
          image_version = var.image_version
          type          = var.flavor_name
        }
        volume_size = "50Gi"
        zones       = var.wg2_zones
      },
    ]
  }
}

variable "cloud" {
  type = string
}

variable "project_id" {
  type = string
}

variable "region" {
  type = string
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

variable "wg2_zones" {
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
