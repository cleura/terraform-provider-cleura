package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// cliCredentialsEnvelope mirrors version 1 of the envelope printed by
// `cleura config get-credentials` — the CLI's stable tool-integration
// contract. Fields are only added while version is 1.
type cliCredentialsEnvelope struct {
	Version       int    `json:"version"`
	Profile       string `json:"profile"`
	Cloud         string `json:"cloud"`
	Endpoint      string `json:"endpoint"`
	Username      string `json:"username"`
	Token         string `json:"token"`
	Region        string `json:"region"`
	ProjectID     string `json:"project_id"`
	TokenStoredAt string `json:"token_stored_at"`
}

// errCLINoCredentials means the CLI tier has nothing to offer (no binary on
// PATH, or it reported "no usable credentials" via its contractual exit code
// 2). Callers fall through to the regular missing-credential errors.
var errCLINoCredentials = errors.New("no credentials available from the cleura CLI")

const cliTimeout = 5 * time.Second

// cliCredentials asks the cleura CLI for its effective credentials, the last
// tier of the provider's credential chain. Any error other than
// errCLINoCredentials is a malfunction worth a warning diagnostic.
func cliCredentials(ctx context.Context, profile string) (*cliCredentialsEnvelope, error) {
	path, err := exec.LookPath("cleura")
	if err != nil {
		return nil, errCLINoCredentials
	}

	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()

	args := []string{"config", "get-credentials"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return nil, errCLINoCredentials
		}
		return nil, fmt.Errorf("running %s config get-credentials: %w", path, err)
	}

	var envelope cliCredentialsEnvelope
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("parsing cleura CLI credentials: %w", err)
	}
	if envelope.Version != 1 {
		return nil, fmt.Errorf("cleura CLI credential envelope version %d is not supported by this provider (supported: 1); upgrade the provider or the CLI", envelope.Version)
	}
	if envelope.Username == "" || envelope.Token == "" {
		return nil, errCLINoCredentials
	}
	return &envelope, nil
}

// storedAt parses the envelope's token timestamp; ok is false when the CLI
// did not record one (pre-0.2.0 logins, env-sourced tokens).
func (e *cliCredentialsEnvelope) storedAt() (time.Time, bool) {
	if e.TokenStoredAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, e.TokenStoredAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// roughAge renders a duration for diagnostics.
func roughAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
