#!/usr/bin/env bash

set -e

OPENAPI_SPEC="$(mktemp)"

curl -s https://rest.cleura.cloud/apidoc.json | sed -r '/^.*required": \[\].*$/d' > "${OPENAPI_SPEC}"

# The API client is no longer generated here: the provider consumes the shared
# github.com/cleura/cleura-client-go module. Regenerate the client there.

# Generate a JSON provider spec for Terraform SDK
tfplugingen-openapi generate \
  --config generator_config.yaml \
  --output provider_code_spec.json \
  "${OPENAPI_SPEC}"

# Generate datamodels from the provider spec
tfplugingen-framework generate all \
    --input provider_code_spec.json \
    --output internal/provider
