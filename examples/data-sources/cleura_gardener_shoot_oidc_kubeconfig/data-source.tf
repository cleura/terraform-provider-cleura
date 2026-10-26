# A kubeconfig that signs in to the cluster through Keycloak, with "kube-api"
# as the OIDC client ID. It holds no secret: kubectl opens the browser through
# the kubelogin plugin (kubectl oidc-login) on first use.
data "cleura_gardener_shoot_oidc_kubeconfig" "sso" {
  project_id   = cleura_gardener_shoot.example.project_id
  shoot_name   = cleura_gardener_shoot.example.name
  issuer_alias = "keycloak"
  audience     = "kube-api"

  # Set when the same apply changes the configuration, for example adds the
  # audience, so the kubeconfig is fetched after that change.
  authentication_configuration_name = cleura_gardener_authentication_configuration.sso.name
}

resource "local_sensitive_file" "kubeconfig" {
  content  = data.cleura_gardener_shoot_oidc_kubeconfig.sso.kubeconfig
  filename = "${path.module}/example-cluster-oidc.kubeconfig"
}
