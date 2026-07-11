package ask

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type runnerStep struct {
	args   []string
	stdout string
	err    error
}

type scriptedRunner struct {
	t     *testing.T
	steps []runnerStep
	next  int
}

func newScriptedRunner(t *testing.T, steps ...runnerStep) *scriptedRunner {
	t.Helper()
	return &scriptedRunner{t: t, steps: steps}
}

func (r *scriptedRunner) Run(args ...string) ([]byte, error) {
	r.t.Helper()
	if r.next >= len(r.steps) {
		r.t.Fatalf("unexpected Runner.Run(%q)", args)
	}
	step := r.steps[r.next]
	r.next++
	if !reflect.DeepEqual(args, step.args) {
		r.t.Fatalf("Runner.Run() args = %q, want %q", args, step.args)
	}
	return []byte(step.stdout), step.err
}

func (r *scriptedRunner) assertDone() {
	r.t.Helper()
	if r.next != len(r.steps) {
		r.t.Fatalf("Runner.Run() made %d calls, want %d; next expected args: %q", r.next, len(r.steps), r.steps[r.next].args)
	}
}

func testConfig() Config {
	return Config{
		Version:             1,
		Driver:              "github",
		Inbox:               "owner/inbox",
		Responder:           "alice",
		PollIntervalSeconds: 10,
	}
}

func disableGitContext(t *testing.T) {
	t.Helper()
	original := runGit
	runGit = func(args ...string) ([]byte, error) {
		return nil, errors.New("not in a git repository")
	}
	t.Cleanup(func() { runGit = original })
}

func TestCmdCreatePostsMarkedBodyAndWritesJSON(t *testing.T) {
	disableGitContext(t)
	cfg := testConfig()
	in := CreateInput{
		Title:    "Choose a database",
		Question: "Which database should we ship?",
		Context:  "A is cheaper.",
		Choices:  []string{"A", "B"},
		JSON:     true,
	}
	body := Marker + "\n\n" + BuildIssueBody(in.Question, in.Context, in.Choices, "")
	runner := newScriptedRunner(t, runnerStep{
		args: []string{
			"api", "repos/owner/inbox/issues", "-X", "POST",
			"-f", "title=Choose a database",
			"-f", "body=" + body,
		},
		stdout: `{"number":42,"html_url":"https://github.com/owner/inbox/issues/42"}`,
	})

	var out bytes.Buffer
	if err := CmdCreate(runner, &out, cfg, in); err != nil {
		t.Fatalf("CmdCreate() error = %v", err)
	}
	runner.assertDone()

	var got CreateResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode CmdCreate JSON %q: %v", out.String(), err)
	}
	want := CreateResult{
		ID:       42,
		Status:   StatusPending,
		IssueURL: "https://github.com/owner/inbox/issues/42",
		Next:     "continue_work",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CmdCreate() result = %+v, want %+v", got, want)
	}
	if strings.Count(body, Marker) != 1 {
		t.Fatalf("posted issue body contains Marker %d times, want once", strings.Count(body, Marker))
	}
}

func TestCmdDetailPostsMarkerComment(t *testing.T) {
	cfg := testConfig()
	runner := newScriptedRunner(t, runnerStep{
		args: []string{
			"api", "repos/owner/inbox/issues/42/comments", "-X", "POST",
			"-f", "body=" + Marker + "\nA costs $30; B costs $80.",
		},
		stdout: `{}`,
	})

	var out bytes.Buffer
	if err := CmdDetail(runner, &out, cfg, 42, "A costs $30; B costs $80."); err != nil {
		t.Fatalf("CmdDetail() error = %v", err)
	}
	runner.assertDone()
	if !strings.Contains(out.String(), "https://github.com/owner/inbox/issues/42") {
		t.Fatalf("CmdDetail() output %q does not contain issue URL", out.String())
	}
}

func TestFetchCommentsMapsPaginatedStreamAndDerives(t *testing.T) {
	cfg := testConfig()
	pages := `[{"body":"notification","created_at":"2026-07-11T05:01:00Z","user":{"login":"alice","type":"Bot"}},` +
		`{"body":"` + Marker + `\ndetail","created_at":"2026-07-11T05:02:00Z","user":{"login":"alice","type":"User"}}]` + "\n" +
		`[{"body":"final answer","created_at":"2026-07-11T05:03:00Z","user":{"login":"alice","type":"User"}}]`
	runner := newScriptedRunner(t, runnerStep{
		args:   []string{"api", "repos/owner/inbox/issues/42/comments", "-X", "GET", "--paginate"},
		stdout: pages,
	})

	comments, err := fetchComments(runner, cfg, 42)
	if err != nil {
		t.Fatalf("fetchComments() error = %v", err)
	}
	runner.assertDone()
	if len(comments) != 3 {
		t.Fatalf("fetchComments() returned %d comments, want 3", len(comments))
	}
	if comments[0].Author != "alice" || comments[0].Body != "notification" || !comments[0].IsBot {
		t.Errorf("mapped bot comment = %+v", comments[0])
	}
	wantTime := time.Date(2026, 7, 11, 5, 3, 0, 0, time.UTC)
	if comments[2].CreatedAt != wantTime || comments[2].IsBot {
		t.Errorf("mapped human comment = %+v, want CreatedAt %s and IsBot false", comments[2], wantTime)
	}
	if got, want := Derive(comments, cfg.Responder), (View{Status: StatusAnswered, Answer: "final answer"}); got != want {
		t.Fatalf("Derive(mapped comments) = %+v, want %+v", got, want)
	}
}

func TestCmdShowClosesAnsweredIssueOnlyWhenOpen(t *testing.T) {
	const comments = `[{"body":"ship A","created_at":"2026-07-11T05:03:00Z","user":{"login":"alice","type":"User"}}]`
	tests := []struct {
		name  string
		state string
		patch bool
	}{
		{name: "open", state: "open", patch: true},
		{name: "already closed", state: "closed", patch: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []runnerStep{
				{
					args:   []string{"api", "repos/owner/inbox/issues/42/comments", "-X", "GET", "--paginate"},
					stdout: comments,
				},
				{
					args:   []string{"api", "repos/owner/inbox/issues/42", "-X", "GET"},
					stdout: `{"number":42,"state":"` + tt.state + `"}`,
				},
			}
			if tt.patch {
				steps = append(steps, runnerStep{
					args:   []string{"api", "repos/owner/inbox/issues/42", "-X", "PATCH", "-f", "state=closed"},
					stdout: `{}`,
				})
			}
			runner := newScriptedRunner(t, steps...)

			var out bytes.Buffer
			if err := CmdShow(runner, &out, testConfig(), 42, true); err != nil {
				t.Fatalf("CmdShow() error = %v", err)
			}
			runner.assertDone()
			var got ShowResult
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("decode CmdShow JSON %q: %v", out.String(), err)
			}
			want := ShowResult{ID: 42, Status: StatusAnswered, Answer: "ship A"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("CmdShow() result = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCmdWaitTimeoutIsNormalResult(t *testing.T) {
	originalNow, originalSleep := commandNow, commandSleep
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	commandNow = func() time.Time { return now }
	commandSleep = func(time.Duration) { t.Fatal("CmdWait() slept for a zero timeout") }
	t.Cleanup(func() {
		commandNow = originalNow
		commandSleep = originalSleep
	})

	runner := newScriptedRunner(t, runnerStep{
		args:   []string{"api", "repos/owner/inbox/issues/42/comments", "-X", "GET", "--paginate"},
		stdout: `[]`,
	})
	var out bytes.Buffer
	if err := CmdWait(runner, &out, testConfig(), 42, 0, true); err != nil {
		t.Fatalf("CmdWait() timeout error = %v, want nil", err)
	}
	runner.assertDone()

	var got ShowResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode CmdWait JSON %q: %v", out.String(), err)
	}
	if got.ID != 42 || got.Status != StatusTimeout {
		t.Fatalf("CmdWait() result = %+v, want ID 42 and timeout status", got)
	}
}

func TestCmdWaitDoesNotPollPastDeadline(t *testing.T) {
	originalNow, originalSleep := commandNow, commandSleep
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	commandNow = func() time.Time { return now }
	commandSleep = func(delay time.Duration) { now = now.Add(delay) }
	t.Cleanup(func() {
		commandNow = originalNow
		commandSleep = originalSleep
	})

	runner := newScriptedRunner(t, runnerStep{
		args:   []string{"api", "repos/owner/inbox/issues/42/comments", "-X", "GET", "--paginate"},
		stdout: `[]`,
	})
	var out bytes.Buffer
	if err := CmdWait(runner, &out, testConfig(), 42, 5*time.Second, true); err != nil {
		t.Fatalf("CmdWait() timeout error = %v, want nil", err)
	}
	runner.assertDone()

	var got ShowResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode CmdWait JSON %q: %v", out.String(), err)
	}
	if got.Status != StatusTimeout {
		t.Fatalf("CmdWait() status = %q, want timeout", got.Status)
	}
}

func TestCmdListFiltersPullRequests(t *testing.T) {
	runner := newScriptedRunner(t, runnerStep{
		args: []string{
			"api", "repos/owner/inbox/issues?state=open&per_page=100", "-X", "GET", "--paginate",
		},
		stdout: `[
  {"number":1,"title":"First question","html_url":"https://github.com/owner/inbox/issues/1"},
  {"number":2,"title":"A pull request","html_url":"https://github.com/owner/inbox/pull/2","pull_request":{"url":"api/pr/2"}}
]
[
  {"number":3,"title":"Second question"}
]`,
	})

	var out bytes.Buffer
	if err := CmdList(runner, &out, testConfig(), true); err != nil {
		t.Fatalf("CmdList() error = %v", err)
	}
	runner.assertDone()

	var got []ListItem
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode CmdList JSON %q: %v", out.String(), err)
	}
	want := []ListItem{
		{ID: 1, Title: "First question", IssueURL: "https://github.com/owner/inbox/issues/1"},
		{ID: 3, Title: "Second question", IssueURL: "https://github.com/owner/inbox/issues/3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CmdList() result = %+v, want %+v", got, want)
	}
}

func TestPutWorkflowUsesExistingSHAForUpsert(t *testing.T) {
	endpoint := "repos/owner/inbox/contents/" + WorkflowPath
	content := base64.StdEncoding.EncodeToString([]byte(WorkflowYAML("alice")))
	runner := newScriptedRunner(t,
		runnerStep{
			args:   []string{"api", endpoint, "-X", "GET"},
			stdout: `{"sha":"existing-sha"}`,
		},
		runnerStep{
			args: []string{
				"api", endpoint, "-X", "PUT",
				"-f", "message=Configure ask notification workflow",
				"-f", "content=" + content,
				"-f", "sha=existing-sha",
			},
			stdout: `{}`,
		},
	)

	if err := putWorkflow(runner, "owner/inbox", "alice"); err != nil {
		t.Fatalf("putWorkflow() error = %v", err)
	}
	runner.assertDone()
}

func TestPutWorkflowCreatesMissingFileWithoutSHA(t *testing.T) {
	endpoint := "repos/owner/inbox/contents/" + WorkflowPath
	content := base64.StdEncoding.EncodeToString([]byte(WorkflowYAML("alice")))
	runner := newScriptedRunner(t,
		runnerStep{
			args: []string{"api", endpoint, "-X", "GET"},
			err:  errors.New("HTTP 404: Not Found"),
		},
		runnerStep{
			args: []string{
				"api", endpoint, "-X", "PUT",
				"-f", "message=Configure ask notification workflow",
				"-f", "content=" + content,
			},
			stdout: `{}`,
		},
	)

	if err := putWorkflow(runner, "owner/inbox", "alice"); err != nil {
		t.Fatalf("putWorkflow() error = %v", err)
	}
	runner.assertDone()
}

func TestCmdInitRunsSetupAndVerifiesReply(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workflowEndpoint := "repos/owner/inbox/contents/" + WorkflowPath
	workflowContent := base64.StdEncoding.EncodeToString([]byte(WorkflowYAML("alice")))
	testBody := Marker + "\n\n" + BuildIssueBody(
		"ask の初期設定を確認します。この issue に何でもよいので返信してください。",
		"", nil, "",
	)
	runner := newScriptedRunner(t,
		runnerStep{args: []string{"auth", "status"}},
		runnerStep{args: []string{"repo", "view", "owner/inbox"}},
		runnerStep{
			args:   []string{"api", workflowEndpoint, "-X", "GET"},
			stdout: `{"sha":"old-sha"}`,
		},
		runnerStep{
			args: []string{
				"api", workflowEndpoint, "-X", "PUT",
				"-f", "message=Configure ask notification workflow",
				"-f", "content=" + workflowContent,
				"-f", "sha=old-sha",
			},
		},
		runnerStep{
			args: []string{
				"api", "repos/owner/inbox/issues", "-X", "POST",
				"-f", "title=ask init test",
				"-f", "body=" + testBody,
			},
			stdout: `{"number":7,"html_url":"https://github.com/owner/inbox/issues/7"}`,
		},
		runnerStep{
			args:   []string{"api", "repos/owner/inbox/issues/7/comments", "-X", "GET", "--paginate"},
			stdout: `[{"body":"looks good","created_at":"2026-07-11T05:03:00Z","user":{"login":"alice","type":"User"}}]`,
		},
		runnerStep{
			args:   []string{"api", "repos/owner/inbox/issues/7", "-X", "GET"},
			stdout: `{"number":7,"state":"open"}`,
		},
		runnerStep{
			args: []string{"api", "repos/owner/inbox/issues/7", "-X", "PATCH", "-f", "state=closed"},
		},
	)

	var out bytes.Buffer
	if err := CmdInit(runner, &out, "owner/inbox", "alice", time.Minute); err != nil {
		t.Fatalf("CmdInit() error = %v", err)
	}
	runner.assertDone()
	if !strings.Contains(out.String(), "ask init verified") || !strings.Contains(out.String(), "https://github.com/owner/inbox/issues/7") {
		t.Fatalf("CmdInit() output = %q, want verification and issue URL", out.String())
	}
	wantConfig := testConfig()
	if got, err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig() after init: %v", err)
	} else if !reflect.DeepEqual(got, wantConfig) {
		t.Fatalf("saved config = %+v, want %+v", got, wantConfig)
	}
}

func TestCmdInitVerificationTimeoutIsClearError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workflowEndpoint := "repos/owner/inbox/contents/" + WorkflowPath
	workflowContent := base64.StdEncoding.EncodeToString([]byte(WorkflowYAML("alice")))
	testBody := Marker + "\n\n" + BuildIssueBody(
		"ask の初期設定を確認します。この issue に何でもよいので返信してください。",
		"", nil, "",
	)
	runner := newScriptedRunner(t,
		runnerStep{args: []string{"auth", "status"}},
		runnerStep{args: []string{"repo", "view", "owner/inbox"}, err: errors.New("not found")},
		runnerStep{args: []string{"repo", "create", "owner/inbox", "--private"}},
		runnerStep{args: []string{"api", workflowEndpoint, "-X", "GET"}, err: errors.New("HTTP 404: Not Found")},
		runnerStep{
			args: []string{
				"api", workflowEndpoint, "-X", "PUT",
				"-f", "message=Configure ask notification workflow",
				"-f", "content=" + workflowContent,
			},
		},
		runnerStep{
			args: []string{
				"api", "repos/owner/inbox/issues", "-X", "POST",
				"-f", "title=ask init test",
				"-f", "body=" + testBody,
			},
			stdout: `{"number":8,"html_url":"https://github.com/owner/inbox/issues/8"}`,
		},
		runnerStep{
			args:   []string{"api", "repos/owner/inbox/issues/8/comments", "-X", "GET", "--paginate"},
			stdout: `[]`,
		},
	)

	var out bytes.Buffer
	err := CmdInit(runner, &out, "owner/inbox", "alice", 0)
	if err == nil || !strings.Contains(err.Error(), "verification timed out") || !strings.Contains(err.Error(), "issues/8") {
		t.Fatalf("CmdInit() error = %v, want clear verification timeout with issue URL", err)
	}
	runner.assertDone()
}

func TestRepositoryFromRemote(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
		ok     bool
	}{
		{name: "scp SSH", remote: "git@github.com:owner/repo.git", want: "owner/repo", ok: true},
		{name: "SSH URL", remote: "ssh://git@github.com/owner/repo.git", want: "owner/repo", ok: true},
		{name: "HTTPS", remote: "https://github.com/owner/repo.git", want: "owner/repo", ok: true},
		{name: "HTTPS no suffix", remote: "https://github.com/owner/repo", want: "owner/repo", ok: true},
		{name: "nested path", remote: "git@github.com:owner/team/repo.git", ok: false},
		{name: "local path", remote: "/home/alice/repo", ok: false},
		{name: "unsupported scheme", remote: "ftp://github.com/owner/repo.git", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := repositoryFromRemote(tt.remote)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("repositoryFromRemote(%q) = (%q, %v), want (%q, %v)", tt.remote, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDeriveFromGit(t *testing.T) {
	original := runGit
	call := 0
	runGit = func(args ...string) ([]byte, error) {
		call++
		switch call {
		case 1:
			if !reflect.DeepEqual(args, []string{"remote", "get-url", "origin"}) {
				t.Fatalf("first git args = %q", args)
			}
			return []byte("git@github.com:owner/project.git\n"), nil
		case 2:
			if !reflect.DeepEqual(args, []string{"rev-parse", "--abbrev-ref", "HEAD"}) {
				t.Fatalf("second git args = %q", args)
			}
			return []byte("feature/contract-tests\n"), nil
		default:
			t.Fatalf("unexpected git call %d: %q", call, args)
			return nil, nil
		}
	}
	t.Cleanup(func() { runGit = original })

	if got, want := deriveFromGit(), "owner/project @ feature/contract-tests"; got != want {
		t.Fatalf("deriveFromGit() = %q, want %q", got, want)
	}
}
