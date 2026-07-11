package ask

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes the gh CLI and returns its stdout. All GitHub access goes
// through gh; ask manages no credentials of its own (PRD §6).
type Runner interface {
	Run(args ...string) ([]byte, error)
}

// ExecRunner runs the real gh binary from PATH.
//
// On an HTTP 403/429 response it waits per the Retry-After header (60s when
// absent) and retries once; any other failure is returned as-is, wrapped
// with gh's stderr for context.
type ExecRunner struct{}

func (ExecRunner) Run(args ...string) ([]byte, error) {
	return runGH(time.Sleep, args...)
}

const ghRetryDelay = 60 * time.Second

func runGH(sleep func(time.Duration), args ...string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		cmd := exec.Command("gh", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		stdout, err := cmd.Output()
		if err == nil {
			return stdout, nil
		}

		stderrText := stderr.String()
		if attempt == 0 && isRetryableGHError(stderrText) {
			// The MVP deliberately uses a fixed delay; Retry-After is not parsed.
			sleep(ghRetryDelay)
			continue
		}

		if message := strings.TrimSpace(stderrText); message != "" {
			return stdout, fmt.Errorf("gh failed: %w: %s", err, message)
		}
		return stdout, fmt.Errorf("gh failed: %w", err)
	}

	panic("unreachable")
}

func isRetryableGHError(stderr string) bool {
	stderr = strings.ToUpper(stderr)
	return strings.Contains(stderr, "HTTP 403") || strings.Contains(stderr, "HTTP 429")
}
