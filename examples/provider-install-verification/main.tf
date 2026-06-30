terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "your-project-id"
}

# Reference example: every optional cleura_gardener_shoot attribute illustrated.
# Values below match public cloud / Sto2 cloud profile (see GET .../cloud-profiles).
resource "cleura_gardener_shoot" "example" {
  name               = "kekwait2"
  kubernetes_version = "1.35.6"

  allowed_cidrs = []

  enable_ha_control_plane = true

  hibernation_schedules = [
    {
      # Cron: weekdays 20:00–08:00 UTC (hibernate overnight)
      start = "0 20 * * 1-5"
      end   = "0 8 * * 1-5"
    },
  ]

  maintenance = {
    auto_update = {
      kubernetes_version    = true
      machine_image_version = true
    }
    time_window = {
      begin = "020000+0000"
      end   = "060000+0000"
    }
  }

  # Cilium networking (newly exposed). All cilium_provider_config fields are set so
  # you can exercise the full block. To test the churn concern, drop individual
  # cilium fields and re-plan (watch for "known after apply"); to test immutability,
  # change `type` (should force replacement) vs. flipping a cilium field (in-place).
  networking = {
    type = "cilium" # calico | cilium
    cilium_provider_config = {
      debug                           = true
      hubble_enabled                  = true
      policy_audit_mode               = false
      tunnel                          = "vxlan" # disabled | geneve | vxlan
      encryption_enabled              = true
      encryption_mode                 = "wireguard" # only accepted value
      encryption_node_to_node_enabled = true
      encryption_strict_mode_enabled  = true
    }
  }

  shoot_provider = {
    infrastructure_config = {
      floating_pool_name = "ext-net"
      # Optional: use when attaching to an existing network (omit to let Cleura create one).
      # network_id           = "00000000-0000-0000-0000-000000000001"
      # router_id            = "00000000-0000-0000-0000-000000000002"
      # workers_network_cidr = "10.250.0.0/16"
    }
    load_balancer_provider = "amphora"

    workers = [
      {
        name = "wg-primary"
        machine = {
          image_name    = "gardenlinux"
          image_version = "1877.19.0"
          type          = "b.2c4gb"
        }
        minimum     = 2
        maximum     = 4
        max_surge   = 1
        volume_size = "50Gi"
        zones       = ["nova"]

        labels = [
          { key = "workload", value = "api" },
          { key = "env", value = "dev" },
        ]

        annotations = [
          { key = "owner", value = "platform-team" },
          { key = "cost-center", value = "engineering" },
        ]

        taints = [
          { key = "dedicated", value = "api", effect = "NoSchedule" },
          { key = "maintenance", value = "true", effect = "PreferNoSchedule" },
          { key = "drain", value = "batch", effect = "NoExecute" },
        ]
      },
      {
        name = "wg-batch"
        machine = {
          image_name    = "gardenlinux"
          image_version = "1877.19.0"
          type          = "b.4c8gb"
        }
        minimum     = 1
        maximum     = 3
        max_surge   = 1
        volume_size = "100Gi"
        zones       = ["nova"]

        labels = [
          { key = "workload", value = "batch" },
        ]

        annotations = [
          { key = "owner", value = "data-team" },
        ]

        taints = [
          { key = "workload", value = "batch", effect = "NoSchedule" },
        ]
      },
    ]
  }
}

resource "cleura_gardener_shoot_kubeconfig" "example" {
  expiration_seconds = 3600
  shoot_name         = cleura_gardener_shoot.example.name
}
