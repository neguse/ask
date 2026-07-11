package ask

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// JSON output shapes (PRD §4). Human-readable output (without --json) is a
// short free-form summary of the same fields plus the issue URL.

type CreateResult struct {
	ID       int    `json:"id"`
	Status   Status `json:"status"`
	IssueURL string `json:"issue_url"`
	Next     string `json:"next"`
}

type ShowResult struct {
	ID      int    `json:"id"`
	Status  Status `json:"status"`
	Request string `json:"request,omitempty"`
	Answer  string `json:"answer,omitempty"`
	Next    string `json:"next,omitempty"`
}

type ListItem struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	IssueURL string `json:"issue_url"`
}

type CreateInput struct {
	Title    string
	Question string
	Context  string
	Choices  []string
	JSON     bool
	Wait     bool
	Timeout  time.Duration
}

// CmdInit performs one-time setup (PRD §3):
//  1. verify `gh auth status`;
//  2. create the private inbox repo unless it exists (`gh repo view` /
//     `gh repo create`);
//  3. push WorkflowYAML to WorkflowPath via the contents API;
//  4. save Config (driver "github", poll interval 10);
//  5. create a test issue and wait up to timeout for a human reply, so that
//     notification delivery and comment reading are verified end to end.
func CmdInit(r Runner, out io.Writer, inbox, responder string, timeout time.Duration) error {
	if strings.TrimSpace(inbox) == "" || strings.TrimSpace(responder) == "" {
		return errors.New("inbox and responder are required")
	}
	if timeout < 0 {
		return errors.New("init timeout must not be negative")
	}
	if _, err := r.Run("auth", "status"); err != nil {
		return fmt.Errorf("check gh authentication: %w", err)
	}

	if _, err := r.Run("repo", "view", inbox); err != nil {
		if _, createErr := r.Run("repo", "create", inbox, "--private"); createErr != nil {
			return fmt.Errorf("create inbox repository %q after repo view failed (%v): %w", inbox, err, createErr)
		}
	}
	if err := putWorkflow(r, inbox, responder); err != nil {
		return err
	}

	cfg := Config{
		Version:             1,
		Driver:              "github",
		Inbox:               inbox,
		Responder:           responder,
		PollIntervalSeconds: 10,
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	testBody := Marker + "\n\n" + BuildIssueBody(
		"ask の初期設定を確認します。この issue に何でもよいので返信してください。",
		"", nil, "",
	)
	issue, err := createIssue(r, cfg, "ask init test", testBody)
	if err != nil {
		return fmt.Errorf("create init verification issue: %w", err)
	}
	if _, err := fmt.Fprintf(out, "ask init: reply to test issue #%d: %s\n", issue.Number, issueURL(cfg, issue)); err != nil {
		return fmt.Errorf("write init result: %w", err)
	}

	view, err := waitForView(r, cfg, issue.Number, timeout)
	if err != nil {
		return fmt.Errorf("verify init reply: %w", err)
	}
	if view.Status == StatusTimeout {
		return fmt.Errorf("ask init verification timed out after %s; no reply was read from %s", timeout, issueURL(cfg, issue))
	}
	if _, err := fmt.Fprintf(out, "ask init verified: reply read from #%d (%s)\n", issue.Number, view.Status); err != nil {
		return fmt.Errorf("write init verification result: %w", err)
	}
	return nil
}

// CmdCreate files a question as a new inbox issue and returns immediately
// (PRD §4.1). The from line is derived from the cwd's git remote "origin"
// and current branch, and omitted outside a git repo. When in.Wait is set,
// it continues into the same loop as CmdWait.
func CmdCreate(r Runner, out io.Writer, cfg Config, in CreateInput) error {
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("question title is required")
	}
	if in.Wait && in.Timeout < 0 {
		return errors.New("wait timeout must not be negative")
	}
	question := in.Question
	if question == "" {
		question = in.Title
	}
	body := Marker + "\n\n" + BuildIssueBody(question, in.Context, in.Choices, deriveFromGit())
	issue, err := createIssue(r, cfg, in.Title, body)
	if err != nil {
		return err
	}

	created := CreateResult{
		ID:       issue.Number,
		Status:   StatusPending,
		IssueURL: issueURL(cfg, issue),
		Next:     "continue_work",
	}
	if err := writeCreateResult(out, created, in.JSON); err != nil {
		return err
	}
	if in.Wait {
		return CmdWait(r, out, cfg, issue.Number, in.Timeout, in.JSON)
	}
	return nil
}

// CmdDetail posts supplementary detail as a Marker comment (PRD §4.4); the
// notify workflow re-mentions the responder.
func CmdDetail(r Runner, out io.Writer, cfg Config, id int, text string) error {
	if id <= 0 {
		return errors.New("issue ID must be positive")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("detail text is required")
	}
	endpoint := fmt.Sprintf("repos/%s/issues/%d/comments", cfg.Inbox, id)
	if _, err := r.Run("api", endpoint, "-X", "POST", "-f", "body="+Marker+"\n"+text); err != nil {
		return fmt.Errorf("post detail to issue #%d: %w", id, err)
	}
	if _, err := fmt.Fprintf(out, "posted detail to #%d: %s\n", id, issueURLForID(cfg, id)); err != nil {
		return fmt.Errorf("write detail result: %w", err)
	}
	return nil
}

// CmdWait polls the issue every cfg.PollIntervalSeconds until the derived
// status is answered or needs_detail, or timeout elapses (PRD §4.3). The
// timeout outcome is a normal result (status "timeout", exit code 0), not an
// error.
func CmdWait(r Runner, out io.Writer, cfg Config, id int, timeout time.Duration, jsonOut bool) error {
	if id <= 0 {
		return errors.New("issue ID must be positive")
	}
	if timeout < 0 {
		return errors.New("wait timeout must not be negative")
	}
	view, err := waitForView(r, cfg, id, timeout)
	if err != nil {
		return err
	}
	return writeShowResult(out, showResult(id, view), issueURLForID(cfg, id), jsonOut)
}

// CmdShow derives the current state from the comment thread without waiting
// (PRD §4.5). Observing answered closes the issue (idempotent: an already
// closed issue stays closed and remains readable).
func CmdShow(r Runner, out io.Writer, cfg Config, id int, jsonOut bool) error {
	if id <= 0 {
		return errors.New("issue ID must be positive")
	}
	view, err := fetchView(r, cfg, id)
	if err != nil {
		return err
	}
	if view.Status == StatusAnswered {
		if err := closeIssueIfOpen(r, cfg, id); err != nil {
			return err
		}
	}
	return writeShowResult(out, showResult(id, view), issueURLForID(cfg, id), jsonOut)
}

// CmdList lists open inbox issues, i.e. unanswered questions (PRD §4.5).
func CmdList(r Runner, out io.Writer, cfg Config, jsonOut bool) error {
	endpoint := fmt.Sprintf("repos/%s/issues?state=open&per_page=100", cfg.Inbox)
	raw, err := r.Run("api", endpoint, "-X", "GET", "--paginate")
	if err != nil {
		return fmt.Errorf("list open inbox issues: %w", err)
	}
	issues, err := decodeConcatenated[apiIssue](raw)
	if err != nil {
		return fmt.Errorf("decode open issue list: %w", err)
	}
	items := make([]ListItem, 0, len(issues))
	for _, issue := range issues {
		if len(issue.PullRequest) != 0 {
			continue
		}
		items = append(items, ListItem{
			ID:       issue.Number,
			Title:    issue.Title,
			IssueURL: issueURL(cfg, issue),
		})
	}
	if jsonOut {
		if err := json.NewEncoder(out).Encode(items); err != nil {
			return fmt.Errorf("write issue list: %w", err)
		}
		return nil
	}
	if len(items) == 0 {
		if _, err := fmt.Fprintln(out, "no open questions"); err != nil {
			return fmt.Errorf("write issue list: %w", err)
		}
		return nil
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(out, "#%d %s: %s\n", item.ID, item.Title, item.IssueURL); err != nil {
			return fmt.Errorf("write issue list: %w", err)
		}
	}
	return nil
}

type apiIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	HTMLURL     string          `json:"html_url"`
	State       string          `json:"state"`
	PullRequest json.RawMessage `json:"pull_request"`
}

type apiComment struct {
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

var (
	commandNow   = time.Now
	commandSleep = time.Sleep
	runGit       = func(args ...string) ([]byte, error) { return exec.Command("git", args...).Output() }
)

func createIssue(r Runner, cfg Config, title, body string) (apiIssue, error) {
	endpoint := fmt.Sprintf("repos/%s/issues", cfg.Inbox)
	raw, err := r.Run("api", endpoint, "-X", "POST", "-f", "title="+title, "-f", "body="+body)
	if err != nil {
		return apiIssue{}, fmt.Errorf("create inbox issue: %w", err)
	}
	var issue apiIssue
	if err := json.Unmarshal(raw, &issue); err != nil {
		return apiIssue{}, fmt.Errorf("decode created issue: %w", err)
	}
	if issue.Number <= 0 {
		return apiIssue{}, errors.New("decode created issue: response has no positive issue number")
	}
	return issue, nil
}

func fetchView(r Runner, cfg Config, id int) (View, error) {
	comments, err := fetchComments(r, cfg, id)
	if err != nil {
		return View{}, err
	}
	return Derive(comments, cfg.Responder), nil
}

func fetchComments(r Runner, cfg Config, id int) ([]Comment, error) {
	endpoint := fmt.Sprintf("repos/%s/issues/%d/comments", cfg.Inbox, id)
	raw, err := r.Run("api", endpoint, "-X", "GET", "--paginate")
	if err != nil {
		return nil, fmt.Errorf("list comments for issue #%d: %w", id, err)
	}

	items, err := decodeConcatenated[apiComment](raw)
	if err != nil {
		return nil, fmt.Errorf("decode comments for issue #%d: %w", id, err)
	}
	comments := make([]Comment, 0, len(items))
	for _, item := range items {
		comments = append(comments, Comment{
			Author:    item.User.Login,
			Body:      item.Body,
			CreatedAt: item.CreatedAt,
			IsBot:     item.User.Type == "Bot",
		})
	}
	return comments, nil
}

// decodeConcatenated decodes the concatenated JSON arrays that
// `gh api --paginate` emits, one array per page.
func decodeConcatenated[T any](raw []byte) ([]T, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	items := make([]T, 0)
	for {
		var page []T
		if err := decoder.Decode(&page); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		items = append(items, page...)
	}
	return items, nil
}

func waitForView(r Runner, cfg Config, id int, timeout time.Duration) (View, error) {
	deadline := commandNow().Add(timeout)
	interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	if interval < 0 {
		return View{}, errors.New("poll interval must not be negative")
	}

	firstPoll := true
	for {
		if !firstPoll && !commandNow().Before(deadline) {
			return View{Status: StatusTimeout}, nil
		}
		firstPoll = false
		view, err := fetchView(r, cfg, id)
		if err != nil {
			return View{}, err
		}
		switch view.Status {
		case StatusAnswered:
			if err := closeIssueIfOpen(r, cfg, id); err != nil {
				return View{}, err
			}
			return view, nil
		case StatusNeedsDetail:
			return view, nil
		}

		remaining := deadline.Sub(commandNow())
		if remaining <= 0 {
			return View{Status: StatusTimeout}, nil
		}
		delay := interval
		if delay > remaining {
			delay = remaining
		}
		if delay > 0 {
			commandSleep(delay)
		}
	}
}

func closeIssueIfOpen(r Runner, cfg Config, id int) error {
	endpoint := fmt.Sprintf("repos/%s/issues/%d", cfg.Inbox, id)
	raw, err := r.Run("api", endpoint, "-X", "GET")
	if err != nil {
		return fmt.Errorf("read issue #%d before closing: %w", id, err)
	}
	var issue apiIssue
	if err := json.Unmarshal(raw, &issue); err != nil {
		return fmt.Errorf("decode issue #%d before closing: %w", id, err)
	}
	if strings.EqualFold(issue.State, "closed") {
		return nil
	}
	if _, err := r.Run("api", endpoint, "-X", "PATCH", "-f", "state=closed"); err != nil {
		return fmt.Errorf("close answered issue #%d: %w", id, err)
	}
	return nil
}

func showResult(id int, view View) ShowResult {
	result := ShowResult{
		ID:      id,
		Status:  view.Status,
		Request: view.Request,
		Answer:  view.Answer,
	}
	if view.Status == StatusNeedsDetail {
		result.Next = fmt.Sprintf("ask detail %d", id)
	}
	return result
}

func writeCreateResult(out io.Writer, result CreateResult, jsonOut bool) error {
	if jsonOut {
		if err := json.NewEncoder(out).Encode(result); err != nil {
			return fmt.Errorf("write create result: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(out, "created question #%d (%s): %s\n", result.ID, result.Status, result.IssueURL); err != nil {
		return fmt.Errorf("write create result: %w", err)
	}
	return nil
}

func writeShowResult(out io.Writer, result ShowResult, issueURL string, jsonOut bool) error {
	if jsonOut {
		if err := json.NewEncoder(out).Encode(result); err != nil {
			return fmt.Errorf("write question result: %w", err)
		}
		return nil
	}
	switch result.Status {
	case StatusAnswered:
		_, err := fmt.Fprintf(out, "#%d answered: %s (%s)\n", result.ID, result.Answer, issueURL)
		if err != nil {
			return fmt.Errorf("write question result: %w", err)
		}
	case StatusNeedsDetail:
		if result.Request == "" {
			_, err := fmt.Fprintf(out, "#%d needs detail: %s\n", result.ID, issueURL)
			if err != nil {
				return fmt.Errorf("write question result: %w", err)
			}
		} else {
			_, err := fmt.Fprintf(out, "#%d needs detail (%s): %s\n", result.ID, result.Request, issueURL)
			if err != nil {
				return fmt.Errorf("write question result: %w", err)
			}
		}
	case StatusTimeout:
		_, err := fmt.Fprintf(out, "#%d timeout: %s\n", result.ID, issueURL)
		if err != nil {
			return fmt.Errorf("write question result: %w", err)
		}
	default:
		_, err := fmt.Fprintf(out, "#%d pending: %s\n", result.ID, issueURL)
		if err != nil {
			return fmt.Errorf("write question result: %w", err)
		}
	}
	return nil
}

func putWorkflow(r Runner, inbox, responder string) error {
	endpoint := fmt.Sprintf("repos/%s/contents/%s", inbox, WorkflowPath)
	sha := ""
	raw, err := r.Run("api", endpoint, "-X", "GET")
	if err == nil {
		var existing struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("decode existing workflow metadata: %w", err)
		}
		sha = existing.SHA
	} else if !isNotFoundError(err) {
		return fmt.Errorf("read existing workflow: %w", err)
	}

	content := base64.StdEncoding.EncodeToString([]byte(WorkflowYAML(responder)))
	args := []string{
		"api", endpoint, "-X", "PUT",
		"-f", "message=Configure ask notification workflow",
		"-f", "content=" + content,
	}
	if sha != "" {
		args = append(args, "-f", "sha="+sha)
	}
	if _, err := r.Run(args...); err != nil {
		return fmt.Errorf("push notify workflow: %w", err)
	}
	return nil
}

func isNotFoundError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "404") || strings.Contains(message, "not found")
}

func deriveFromGit() string {
	remoteOutput, err := runGit("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	repository, ok := repositoryFromRemote(strings.TrimSpace(string(remoteOutput)))
	if !ok {
		return ""
	}
	branchOutput, err := runGit("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(branchOutput))
	if branch == "" {
		return ""
	}
	return repository + " @ " + branch
}

func repositoryFromRemote(remote string) (string, bool) {
	var repositoryPath string
	if parsed, err := url.Parse(remote); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "ssh", "git":
			repositoryPath = parsed.Path
		default:
			return "", false
		}
	} else if colon := strings.Index(remote, ":"); colon > 0 && strings.Contains(remote[:colon], "@") {
		repositoryPath = remote[colon+1:]
	} else {
		return "", false
	}

	repositoryPath = strings.Trim(strings.TrimSpace(repositoryPath), "/")
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func issueURL(cfg Config, issue apiIssue) string {
	if issue.HTMLURL != "" {
		return issue.HTMLURL
	}
	return issueURLForID(cfg, issue.Number)
}

func issueURLForID(cfg Config, id int) string {
	return "https://github.com/" + strings.Trim(cfg.Inbox, "/") + "/issues/" + strconv.Itoa(id)
}
