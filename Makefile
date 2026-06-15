# Run acceptance tests with 60-minute timeout per test (shoot creation/reconciliation can be slow).
# Requires TF_ACC=1, provider env vars (CLEURA_*), and shoot test vars (CLEURA_TEST_KUBERNETES_VERSION, CLEURA_TEST_IMAGE_VERSION).
testacc:
	TF_ACC=1 go test -timeout 60m -v -count=1 ./internal/provider/...
