#!/usr/bin/env bash
set -euo pipefail

read -p "Username: " CLEURA_API_USERNAME
read -s -p "Password: " CLEURA_API_PASSWORD
echo

CLOUD="${CLEURA_CLOUD:-}"
if [[ -z "$CLOUD" ]]; then
  read -p "Cloud (public/compliant) [public]: " CLOUD
  CLOUD="${CLOUD:-public}"
fi

case "$CLOUD" in
  public)
    API_URL="${CLEURA_API_URL:-https://rest.cleura.cloud}"
    ;;
  compliant)
    API_URL="${CLEURA_API_URL:-https://rest.compliant.cleura.cloud}"
    ;;
  *)
    echo "Unknown cloud: $CLOUD (use public or compliant)" >&2
    exit 1
    ;;
esac

echo "Using API: $API_URL"

VERIFICATION=$(curl -s \
  -H "Content-Type: application/json" \
  -X POST \
  -d '{"auth":{"login":"'"${CLEURA_API_USERNAME}"'","password":"'"${CLEURA_API_PASSWORD}"'"}}' \
  "${API_URL}/auth/v1/tokens" | jq -r '.verification')

read -p "Do you have 2-factor authentication enabled? (y/n): " HAS_2FA

if [[ "$HAS_2FA" =~ ^[Yy]$ ]]; then
  curl -s \
    -H "Content-Type: application/json" \
    -X POST \
    -d '{"request2fa":{"login":"'"${CLEURA_API_USERNAME}"'","verification":"'"${VERIFICATION}"'"}}' \
    "${API_URL}/auth/v1/tokens/request2facode"

  read -p "SMS code: " CODE
else
  CODE=""
fi

curl -s \
  -H "Content-Type: application/json" \
  -X POST \
  -d '{"verify2fa":{"login":"'"${CLEURA_API_USERNAME}"'","verification":"'"${VERIFICATION}"'","code":'"${CODE}"'}}' \
  "${API_URL}/auth/v1/tokens/verify2fa"
