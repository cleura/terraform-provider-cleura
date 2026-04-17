# Run acceptance tests with 60-minute timeout per test (shoot creation/reconciliation can be slow).
# Requires TF_ACC=1 and Cleura API credentials.
testacc:
	TF_ACC=1 go test -timeout 60m -v -count=1 ./internal/provider/...
