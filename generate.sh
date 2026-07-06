#!/usr/bin/env bash

set -e

OPENAPI_SPEC="$(mktemp)"
OPENAPI_SPEC_30="$(mktemp)"

curl -s https://rest.cleura.cloud/apidoc.json | sed -r '/^.*required": \[\].*$/d' > "${OPENAPI_SPEC}"
openapi_downgrade "${OPENAPI_SPEC}" "${OPENAPI_SPEC_30}"

# Generate the API client
oapi-codegen -config client-oapi-config.yaml -include-tags Gardener,OpenStack_Identity "${OPENAPI_SPEC_30}"

# Generate a JSON provider spec for Terraform SDK
tfplugingen-openapi generate \
  --config generator_config.yaml \
  --output provider_code_spec.json \
  "${OPENAPI_SPEC}"

# Generate datamodels from the provider spec
tfplugingen-framework generate all \
    --input provider_code_spec.json \
    --output internal/provider
