# Unit tests (no cloud credentials required).
test:
	go test -v -cover ./...

# Run acceptance tests with 60-minute timeout per test (shoot creation/reconciliation can be slow).
# Requires TF_ACC=1, provider env vars (CLEURA_*), and shoot test vars (CLEURA_TEST_KUBERNETES_VERSION, CLEURA_TEST_IMAGE_VERSION).
testacc:
	TF_ACC=1 go test -timeout 60m -v -count=1 ./internal/provider/...

# Generate Terraform Registry documentation (docs/) from the provider schema and
# the examples/ directory. No cloud credentials required. Run after any schema or
# examples/ change so the registry docs stay in sync.
docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest generate -provider-name cleura

.PHONY: test testacc docs
