terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
    openstack = {
      source = "terraform-provider-openstack/openstack"
    }
  }
}

# The volume, managed by the OpenStack provider. Its volume type must support
# the Recovery service.
resource "openstack_blockstorage_volume_v3" "data" {
  name        = "data"
  size        = 100
  volume_type = "cbs"
}

# Switch on the Recovery service for the volume and keep recovery points for
# 10 days (10 or 30).
resource "cleura_openstack_volume_recovery_service" "data" {
  volume_id      = openstack_blockstorage_volume_v3.data.id
  retention_days = 10
}
