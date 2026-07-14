## What & why

<!-- What does this change, and why? Link any related issue. -->

## Checklist

- [ ] `go build ./...`, `go vet ./...`, and `go test ./...` pass
- [ ] `make docs` re-run if the schema or examples changed (regenerated `docs/` committed)
- [ ] Acceptance tests (`TF_ACC=1 go test ./...`) run if behaviour changed — they need Cleura credentials and create real resources
- [ ] No secrets, tokens, or personal paths in the diff
