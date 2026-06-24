# A Gardener Kubernetes cluster ("shoot") on Cleura public cloud.
resource "cleura_gardener_shoot" "example" {
  name               = "example-cluster"
  kubernetes_version = "1.35.6"

  # Restrict Kubernetes API server access (optional).
  allowed_cidrs = ["192.168.0.0/16"]

  # Highly available control plane. Note: it cannot be disabled in place once
  # enabled — doing so forces the cluster to be recreated.
  enable_ha_control_plane = true

  # Automatic maintenance window (times are UTC, cron-style).
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

  # Optional hibernation schedules (cron expressions, UTC). Use commas or ranges
  # for multiple weekdays, e.g. "0 20 * * 1-5".
  hibernation_schedules = [
    {
      start = "0 20 * * 1-5" # hibernate on weekday evenings
      end   = "0 8 * * 1-5"  # wake on weekday mornings
    },
  ]

  shoot_provider = {
    load_balancer_provider = "amphora"

    infrastructure_config = {
      floating_pool_name = "ext-net"
      # Attach to an existing network instead of letting Cleura create one:
      # network_id           = "00000000-0000-0000-0000-000000000001"
      # router_id            = "00000000-0000-0000-0000-000000000002"
      # workers_network_cidr = "10.250.0.0/16"
    }

    # Worker groups are matched by name; keep their order stable in config.
    workers = [
      {
        name = "default"
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
          { key = "workload", value = "general" },
        ]

        # Optional node taints:
        # taints = [
        #   { key = "dedicated", value = "api", effect = "NoSchedule" },
        # ]
      },
    ]
  }
}
