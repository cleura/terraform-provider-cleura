# Fetch a short-lived admin kubeconfig for a shoot. The resource is recreated
# automatically once the credential reaches its renewal window.
resource "cleura_gardener_shoot_kubeconfig" "example" {
  # Name of an existing cluster. When the cluster is managed in the same
  # configuration, reference it instead so Terraform orders the two correctly:
  #   shoot_name = cleura_gardener_shoot.example.name
  shoot_name         = "example-cluster"
  expiration_seconds = 3600
}

output "kubeconfig" {
  value     = cleura_gardener_shoot_kubeconfig.example.kubeconfig
  sensitive = true
}
