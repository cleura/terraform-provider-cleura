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
	fakeCLI(t, `echo '{"error": "no credentials"}'; exit 2`)
	if _, err := cliCredentials(context.Background(), ""); !errors.Is(err, errCLINoCredentials) {
		t.Fatalf("exit 2 must map to errCLINoCredentials, got %v", err)
	}
}

func TestCLICredentialsMissingBinaryMeansNone(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := cliCredentials(context.Background(), ""); !errors.Is(err, errCLINoCredentials) {
		t.Fatalf("missing binary must map to errCLINoCredentials, got %v", err)
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
