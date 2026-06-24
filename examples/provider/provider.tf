terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

# Public Cleura cloud, Stockholm (Sto2) region.
# username, token, and project_id may also be supplied via the
# CLEURA_API_USERNAME, CLEURA_API_TOKEN, and CLEURA_PROJECT_ID environment
# variables. project_id is only required for the Gardener resources; it may be
# omitted when using data sources alone.
provider "cleura" {
  cloud      = "public" # "public", "compliant", or a private cloud name
  region     = "Sto2"
  project_id = "8a22c50af68e45c6b4dd7722cce8f93a"

  # username = "..." # or CLEURA_API_USERNAME
  # token    = "..." # or CLEURA_API_TOKEN
}
