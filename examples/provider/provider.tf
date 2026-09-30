terraform {
  required_providers {
    cleura = {
      source  = "cleura/cleura"
      version = "~> 0.3"
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
# region and project_id are never taken from the CLI: set them here (or via
# CLEURA_REGION / CLEURA_PROJECT_ID). project_id is the default for the Gardener
# resources, which can also set their own.
provider "cleura" {
  cloud      = "public" # "public", "compliant", or a private cloud name
  region     = "Sto2"
  project_id = "your-project-id"

  # username = "..." # or CLEURA_API_USERNAME — overrides the CLI
  # token    = "..." # or CLEURA_API_TOKEN    — overrides the CLI
}
