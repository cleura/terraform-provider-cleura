# Fetch a short-lived admin kubeconfig for a shoot. The resource is recreated
# automatically once the credential reaches its renewal window.
resource "cleura_gardener_shoot_kubeconfig" "example" {
  shoot_name         = cleura_gardener_shoot.example.name
  expiration_seconds = 3600
}

output "kubeconfig" {
  value     = cleura_gardener_shoot_kubeconfig.example.kubeconfig
  sensitive = true
}
