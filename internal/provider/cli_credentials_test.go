package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCLI installs a fake `cleura` script as the only binary on PATH and
// returns the directory its invocation arguments are recorded in.
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary tests use shell scripts")
	}
	dir := t.TempDir()
	body := "#!/bin/sh\necho \"$@\" > \"" + dir + "/args\"\n" + script
	if err := os.WriteFile(filepath.Join(dir, "cleura"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestCLICredentialsSuccess(t *testing.T) {
	// Only shell builtins: the fake dir is the entire PATH.
	dir := fakeCLI(t, `echo '{"version":1,"profile":"work","cloud":"compliant","endpoint":"https://rest.compliant.cleura.cloud","username":"svc","token":"tok-1","region":"sto1","project_id":"p1","token_stored_at":"2026-07-08T08:00:00Z"}'`)

	creds, err := cliCredentials(context.Background(), "work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Username != "svc" || creds.Token != "tok-1" || creds.Cloud != "compliant" || creds.Endpoint != "https://rest.compliant.cleura.cloud" {
		t.Errorf("envelope mismatch: %+v", creds)
	}
	if _, ok := creds.storedAt(); !ok {
		t.Error("storedAt should parse")
	}

	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if got := strings.TrimSpace(string(args)); got != "config get-credentials --profile work" {
		t.Errorf("CLI invoked with %q", got)
	}
}

func TestCLICredentialsExitTwoMeansNone(t *testing.T) {
	fakeCLI(t, `echo '{"error": "no credentials: profile \"compliant\" has no token"}'; exit 2`)
	_, err := cliCredentials(context.Background(), "")
	if !errors.Is(err, errCLINoCredentials) {
		t.Fatalf("exit 2 must map to errCLINoCredentials, got %v", err)
	}
	// The CLI's reason (it names the affected profile) must survive.
	if !strings.Contains(err.Error(), `profile "compliant"`) {
		t.Errorf("exit-2 reason should be carried in the error, got %v", err)
	}
}

func TestCLICredentialsStripsCredentialEnv(t *testing.T) {
	// The provider consumed CLEURA_* itself as tier 2; the subprocess must
	// answer purely from CLI state. CLEURA_PROFILE stays (selects state).
	dir := fakeCLI(t, `echo "$CLEURA_API_TOKEN|$CLEURA_CLOUD|$CLEURA_PROFILE" > "$0.env"
echo '{"version":1,"profile":"p","cloud":"public","endpoint":"e","username":"u","token":"t"}'`)
	t.Setenv("CLEURA_API_TOKEN", "leaky-token")
	t.Setenv("CLEURA_CLOUD", "leaky-cloud")
	t.Setenv("CLEURA_PROFILE", "kept-profile")

	if _, err := cliCredentials(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seen, err := os.ReadFile(filepath.Join(dir, "cleura.env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(seen)); got != "||kept-profile" {
		t.Errorf("subprocess env = %q, want credential vars stripped and CLEURA_PROFILE kept", got)
	}
}

func TestCLICredentialsTooOld(t *testing.T) {
	// Released v0.1.0 has no get-credentials: cobra prints an unknown-command
	// error and exits 1. That must map to the friendly upgrade warning, not
	// an opaque malfunction on every plan.
	fakeCLI(t, `echo 'Error: unknown command "get-credentials" for "cleura config"' >&2; exit 1`)
	if _, err := cliCredentials(context.Background(), ""); !errors.Is(err, errCLITooOld) {
		t.Fatalf("unknown command must map to errCLITooOld, got %v", err)
	}
}

func TestCLICredentialsMalfunctionCarriesStderr(t *testing.T) {
	fakeCLI(t, `echo 'Error: parsing config /home/x/config.yaml: yaml: bad' >&2; exit 1`)
	_, err := cliCredentials(context.Background(), "")
	if err == nil || errors.Is(err, errCLINoCredentials) || errors.Is(err, errCLITooOld) {
		t.Fatalf("want malfunction, got %v", err)
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("stderr detail should be surfaced, got %v", err)
	}
}

func TestCLICredentialsMissingBinaryMeansNone(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := cliCredentials(context.Background(), "")
	// Both sentinels must match: NotFound is the install-specific case of
	// NoCredentials, so fall-through logic and install guidance both work.
	if !errors.Is(err, errCLINoCredentials) || !errors.Is(err, errCLINotFound) {
		t.Fatalf("missing binary must map to errCLINotFound (wrapping errCLINoCredentials), got %v", err)
	}
}

func TestCLICredentialsMalfunctions(t *testing.T) {
	tests := []struct {
		name, script, wantErrPart string
	}{
		{"garbage output", `echo "not json"`, "parsing"},
		{"unsupported version", `echo '{"version": 99, "username": "u", "token": "t"}'`, "version 99"},
		{"other exit code", `exit 7`, "get-credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeCLI(t, tt.script)
			_, err := cliCredentials(context.Background(), "")
			if err == nil || errors.Is(err, errCLINoCredentials) || !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Fatalf("want malfunction containing %q, got %v", tt.wantErrPart, err)
			}
		})
	}
}
