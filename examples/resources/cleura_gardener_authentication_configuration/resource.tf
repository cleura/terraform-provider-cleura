# Accept OIDC tokens from a Keycloak realm on Gardener clusters. Users sign in
# as "oidc:keycloak:<username>" and carry their groups as
# "oidc:keycloak:<group>", which is what role bindings refer to.
resource "cleura_gardener_authentication_configuration" "sso" {
  name = "corp-sso"

  issuers = {
    keycloak = {
      url       = "https://keycloak.example.com/realms/platform"
      audiences = ["kube-api"] # the OIDC client ID(s) kubectl signs in with

      # Only needed when the issuer's certificate is not from a public CA.
      # certificate_authority = file("${path.module}/keycloak-ca.pem")

      claim_mappings = {
        username = { claim = "preferred_username" }
        groups   = { claim = "groups" }
      }

      user_validation_rules = [
        { expression = "!user.username.startsWith(\"system:\")", message = "username cannot use the reserved system: prefix" },
      ]
    }
  }

  # When clusters are moved to another configuration and this one is removed
  # in the same apply, update the clusters before deleting it.
  lifecycle {
    create_before_destroy = true
  }
}

# Attach it to a cluster. Changing or removing it updates the cluster in place.
resource "cleura_gardener_shoot" "example" {
  name               = "example-cluster"
  kubernetes_version = "1.35.9"

  authentication_configuration_name = cleura_gardener_authentication_configuration.sso.name

  shoot_provider = {
    infrastructure_config = {
      floating_pool_name = "ext-net"
    }
    load_balancer_provider = "amphora"
    workers = [{
      name = "wg1"
      machine = {
        image_name    = "gardenlinux"
        image_version = "1877.24.0"
        type          = "b.2c4gb"
      }
      volume_size = "50Gi"
    }]
  }
}
