package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "leading flag", args: []string{"--unknown"}},
		{name: "missing create title", args: []string{"create"}},
		{name: "invalid issue ID", args: []string{"show", "zero"}},
		{name: "shorthand cannot override title", args: []string{"question", "--title", "other"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stderr := captureRunStderr(t, tt.args)
			if code != 2 {
				t.Fatalf("run(%q) = %d, want 2; stderr: %s", tt.args, code, stderr)
			}
			if stderr != usage+"\n" {
				t.Fatalf("stderr = %q, want exact usage", stderr)
			}
		})
	}
}

func TestRunAcceptsDocumentedPositionalFlagOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	code, stderr := captureRunStderr(t, []string{"wait", "42", "--timeout", "0", "--json"})
	if code != 1 {
		t.Fatalf("run(wait) = %d, want config error exit 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "ask init") || strings.Contains(stderr, usage) {
		t.Fatalf("stderr = %q, want missing-config error without usage", stderr)
	}
}

func TestRunTreatsUnknownWordAsShorthand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	code, stderr := captureRunStderr(t, []string{"ship it?", "--context", "release"})
	if code != 1 {
		t.Fatalf("run(shorthand) = %d, want config error exit 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "ask init") || strings.Contains(stderr, usage) {
		t.Fatalf("stderr = %q, want missing-config error without usage", stderr)
	}
}

func TestRunShorthandAcceptsChoices(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	code, stderr := captureRunStderr(t, []string{"ship it?", "--context", "release", "--choice", "yes", "--choice", "no", "--json"})
	if code != 1 {
		t.Fatalf("run(shorthand --choice) = %d, want config error exit 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "ask init") || strings.Contains(stderr, usage) {
		t.Fatalf("stderr = %q, want missing-config error without usage", stderr)
	}
}

func captureRunStderr(t *testing.T, args []string) (int, string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	code := run(args)
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	os.Stderr = original
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return code, string(contents)
}
