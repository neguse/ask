package ask

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerReturnsStdout(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
printf '%s' "$*"
`)

	stdout, err := (ExecRunner{}).Run("api", "user")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got, want := string(stdout), "api user"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestExecRunnerIncludesStderrInError(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
printf 'HTTP 500: exploded\n' >&2
exit 1
`)

	_, err := (ExecRunner{}).Run("api", "user")
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if !strings.Contains(err.Error(), "HTTP 500: exploded") {
		t.Fatalf("error %q does not contain gh stderr", err)
	}
}

func TestRunGHWaitsAndRetriesOnceForRateLimit(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "attempts")
	t.Setenv("GH_TEST_STATE", statePath)
	installFakeGH(t, `#!/bin/sh
attempt=0
if [ -f "$GH_TEST_STATE" ]; then
  attempt=$(cat "$GH_TEST_STATE")
fi
attempt=$((attempt + 1))
printf '%s' "$attempt" > "$GH_TEST_STATE"
if [ "$attempt" -eq 1 ]; then
  printf 'http 429: rate limited\n' >&2
  exit 1
fi
printf 'retried'
`)

	var delays []time.Duration
	stdout, err := runGH(func(delay time.Duration) {
		delays = append(delays, delay)
	}, "api", "user")
	if err != nil {
		t.Fatalf("runGH returned error: %v", err)
	}
	if got, want := string(stdout), "retried"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if len(delays) != 1 || delays[0] != 60*time.Second {
		t.Fatalf("retry delays = %v, want [1m0s]", delays)
	}
	if attempts, err := os.ReadFile(statePath); err != nil {
		t.Fatalf("read attempts: %v", err)
	} else if got, want := string(attempts), "2"; got != want {
		t.Fatalf("attempt count = %q, want %q", got, want)
	}
}

func installFakeGH(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
