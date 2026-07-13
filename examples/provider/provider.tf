terraform {
  required_providers {
    cleura = {
      source = "cleura/cleura"
    }
  }
}

# Public Cleura cloud, Stockholm (Sto2) region.
#
# Credentials come from the cleura CLI: run `cleura login` once and the
# provider picks up the token automatically — no username/token needed here.
# To override, set the username/token attributes below or the CLEURA_API_*
# environment variables; both take precedence over the CLI.
#
# region and project_id are always set here (or via CLEURA_REGION /
# CLEURA_PROJECT_ID) and are never taken from the CLI. project_id is only
# required for the Gardener resources.
provider "cleura" {
  cloud      = "public" # "public", "compliant", or a private cloud name
  region     = "Sto2"
  project_id = "your-project-id"

  # username = "..." # or CLEURA_API_USERNAME — overrides the CLI
  # token    = "..." # or CLEURA_API_TOKEN    — overrides the CLI
}
