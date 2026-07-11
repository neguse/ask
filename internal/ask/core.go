// Package ask implements the ask CLI: asynchronous questions from an AI
// to a human, carried over issues in a dedicated GitHub inbox repository.
package ask

import (
	"strings"
	"time"
)

// Marker identifies comments written by the AI side. The AI and the human
// responder share one GitHub account, so authorship alone cannot distinguish
// them; every comment the CLI posts must contain this marker.
const Marker = "<!-- ask:ai -->"

type Status string

const (
	StatusPending     Status = "pending"
	StatusNeedsDetail Status = "needs_detail"
	StatusAnswered    Status = "answered"
	StatusTimeout     Status = "timeout"
)

// Comment is one issue comment. Callers pass comments in creation order.
type Comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
	IsBot     bool
}

// View is the state of a question, derived from its comment thread alone.
// There is no local state; deriving twice must give the same result.
type View struct {
	Status  Status
	Request string // set when Status is needs_detail; empty for a bare "more"
	Answer  string // set when Status is answered
}

// Derive computes the question state from comments (PRD §5).
//
// Only comments whose author equals responder, that are not bots and do not
// contain Marker, count as human turns. The last human turn decides:
// none → pending; a "more" request → needs_detail, unless a Marker comment
// follows it (the AI already replied) → back to pending; anything else →
// answered, with that comment body as Answer.
func Derive(comments []Comment, responder string) View {
	lastHuman := -1
	for i, comment := range comments {
		if comment.Author == responder && !comment.IsBot && !strings.Contains(comment.Body, Marker) {
			lastHuman = i
		}
	}
	if lastHuman == -1 {
		return View{Status: StatusPending}
	}

	more, request := ParseMore(comments[lastHuman].Body)
	if !more {
		return View{Status: StatusAnswered, Answer: comments[lastHuman].Body}
	}

	for _, comment := range comments[lastHuman+1:] {
		if strings.Contains(comment.Body, Marker) {
			return View{Status: StatusPending}
		}
	}
	return View{Status: StatusNeedsDetail, Request: request}
}

// ParseMore reports whether body requests more detail, and returns the
// requested content (empty for a bare request). Recognized after trimming
// surrounding whitespace: "more" (any case) or "もっと詳しく", either bare or
// followed by ":" or "：" and the request text (PRD §4.2). Anything else,
// including text that merely starts with "more" ("moreover ..."), is not a
// more-request.
func ParseMore(body string) (bool, string) {
	body = strings.TrimSpace(body)

	if len(body) >= len("more") && strings.EqualFold(body[:len("more")], "more") {
		if more, request := parseMoreSuffix(body[len("more"):]); more {
			return true, request
		}
	}
	if strings.HasPrefix(body, "もっと詳しく") {
		return parseMoreSuffix(body[len("もっと詳しく"):])
	}

	return false, ""
}

func parseMoreSuffix(suffix string) (bool, string) {
	if suffix == "" {
		return true, ""
	}
	if strings.HasPrefix(suffix, ":") {
		return true, strings.TrimSpace(strings.TrimPrefix(suffix, ":"))
	}
	if strings.HasPrefix(suffix, "：") {
		return true, strings.TrimSpace(strings.TrimPrefix(suffix, "："))
	}
	return false, ""
}

// BuildIssueBody renders the issue body (PRD §4.1): the from line when
// non-empty, the question, optional Context and Choices sections, and the
// fixed reply instructions.
func BuildIssueBody(question, context string, choices []string, from string) string {
	parts := make([]string, 0, 5)
	if from != "" {
		parts = append(parts, "> from: "+from)
	}
	parts = append(parts, question)
	if context != "" {
		parts = append(parts, "## Context\n\n"+context)
	}
	if len(choices) != 0 {
		parts = append(parts, "## Choices\n\n"+strings.Join(choices, " / "))
	}
	parts = append(parts, "この issue にコメントで返信してください。\n追加情報が必要なら「more: 欲しい情報」と返信してください。")
	return strings.Join(parts, "\n\n")
}
