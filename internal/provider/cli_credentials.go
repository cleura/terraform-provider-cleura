package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
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

// errCLINoCredentials means the CLI tier has nothing to offer (the CLI
// reported "no usable credentials" via its contractual exit code 2). It may
// be wrapped with the CLI's own reason. Callers fall through to the regular
// missing-credential errors.
var errCLINoCredentials = errors.New("no credentials available from the cleura CLI")

// errCLINotFound is the special case of errCLINoCredentials where the CLI is
// not installed at all — the fall-through guidance should say "install",
// not "log in". errors.Is matches both sentinels.
var errCLINotFound = fmt.Errorf("%w: the cleura CLI is not installed (not found in PATH)", errCLINoCredentials)

// errCLITooOld means the installed CLI predates the get-credentials contract
// (released v0.1.0 errors with "unknown command"). Worth one clear warning,
// not an opaque malfunction on every plan.
var errCLITooOld = errors.New("the installed cleura CLI does not support 'config get-credentials'; upgrade to cleura v0.2.0 or newer, or set credentials explicitly")

const cliTimeout = 5 * time.Second

// credentialEnvVars are consumed by the provider itself as its second
// credential tier and must not leak into the CLI subprocess: tier 3 answers
// purely from CLI state, or a stale CLEURA_API_TOKEN in the environment would
// half-override an explicitly requested profile and no mismatch warning could
// ever fire. CLEURA_PROFILE and CLEURA_CONFIG stay — they select which CLI
// state to read.
var credentialEnvVars = []string{
	"CLEURA_API_USERNAME",
	"CLEURA_API_TOKEN",
	"CLEURA_API_PASSWORD",
	"CLEURA_API_URL",
	"CLEURA_CLOUD",
	"CLEURA_REGION",
	"CLEURA_PROJECT_ID",
}

// cliCredentials asks the cleura CLI for its stored credentials, the last
// tier of the provider's credential chain. Errors other than
// errCLINoCredentials and errCLITooOld are malfunctions worth a warning.
func cliCredentials(ctx context.Context, profile string) (*cliCredentialsEnvelope, error) {
	path, err := exec.LookPath("cleura")
	if err != nil {
		return nil, errCLINotFound
	}

	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()

	args := []string{"config", "get-credentials"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = strippedEnv()
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			switch {
			case exitErr.ExitCode() == 2:
				// Contractual "nothing stored"; stdout carries the reason.
				if reason := parseCLIErrorReason(out); reason != "" {
					return nil, fmt.Errorf("%w: %s", errCLINoCredentials, reason)
				}
				return nil, errCLINoCredentials
			case strings.Contains(stderr, "unknown command"):
				return nil, errCLITooOld
			default:
				if len(stderr) > 300 {
					stderr = stderr[:300] + "…"
				}
				return nil, fmt.Errorf("running %s config get-credentials: %w: %s", path, err, stderr)
			}
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

// strippedEnv is the process environment minus the provider's own credential
// tier variables.
func strippedEnv() []string {
	env := os.Environ()
	kept := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(credentialEnvVars, name) {
			kept = append(kept, kv)
		}
	}
	return kept
}

// parseCLIErrorReason extracts the message from the CLI's exit-2 JSON
// {"error": ...} payload; empty when it cannot be parsed.
func parseCLIErrorReason(out []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &payload) != nil {
		return ""
	}
	return payload.Error
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
