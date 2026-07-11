package ask

import (
	"strings"
	"testing"
	"time"
)

func TestParseMore(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    bool
		request string
	}{
		{"bare more", "more", true, ""},
		{"bare more upper", "MORE", true, ""},
		{"bare more surrounded by space", "  more \n", true, ""},
		{"more with request", "more: 移行費用を教えて", true, "移行費用を教えて"},
		{"more fullwidth colon", "more：現在の利用量", true, "現在の利用量"},
		{"more colon no space", "more:x", true, "x"},
		{"more colon empty request", "more:", true, ""},
		{"japanese bare", "もっと詳しく", true, ""},
		{"japanese with request", "もっと詳しく: 現在の利用量を教えて", true, "現在の利用量を教えて"},
		{"japanese fullwidth colon", "もっと詳しく：予算は？", true, "予算は？"},
		{"prefix word is not more", "moreover, we should ship A", false, ""},
		{"more inside sentence", "それなら more で", false, ""},
		{"plain answer", "それなら A で。", false, ""},
		{"empty", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, request := ParseMore(tt.body)
			if got != tt.want || request != tt.request {
				t.Errorf("ParseMore(%q) = (%v, %q), want (%v, %q)",
					tt.body, got, request, tt.want, tt.request)
			}
		})
	}
}

func at(min int) time.Time {
	return time.Date(2026, 7, 11, 5, min, 0, 0, time.UTC)
}

func TestDerive(t *testing.T) {
	const responder = "neguse"
	human := func(min int, body string) Comment {
		return Comment{Author: responder, Body: body, CreatedAt: at(min)}
	}
	ai := func(min int, body string) Comment {
		return Comment{Author: responder, Body: Marker + "\n" + body, CreatedAt: at(min)}
	}
	bot := func(min int) Comment {
		return Comment{Author: "github-actions[bot]", Body: "@neguse 返信をお願いします。", CreatedAt: at(min), IsBot: true}
	}

	tests := []struct {
		name     string
		comments []Comment
		want     View
	}{
		{
			"no comments",
			nil,
			View{Status: StatusPending},
		},
		{
			"only bot mention",
			[]Comment{bot(1)},
			View{Status: StatusPending},
		},
		{
			"answered",
			[]Comment{bot(1), human(2, "それなら A で。")},
			View{Status: StatusAnswered, Answer: "それなら A で。"},
		},
		{
			"needs detail",
			[]Comment{bot(1), human(2, "more: 移行費用を教えて")},
			View{Status: StatusNeedsDetail, Request: "移行費用を教えて"},
		},
		{
			"bare more",
			[]Comment{bot(1), human(2, "もっと詳しく")},
			View{Status: StatusNeedsDetail},
		},
		{
			"detail supplied returns to pending",
			[]Comment{bot(1), human(2, "more: 費用は？"), ai(3, "A は約3万円、B は約8万円です")},
			View{Status: StatusPending},
		},
		{
			"full round trip",
			[]Comment{
				bot(1),
				human(2, "more: 費用は？"),
				ai(3, "A は約3万円、B は約8万円です"),
				bot(4),
				human(5, "それなら A で。"),
			},
			View{Status: StatusAnswered, Answer: "それなら A で。"},
		},
		{
			"later human comment wins",
			[]Comment{human(1, "A で。"), human(2, "やっぱり B で。")},
			View{Status: StatusAnswered, Answer: "やっぱり B で。"},
		},
		{
			"other authors ignored",
			[]Comment{{Author: "someoneelse", Body: "B がいいと思う", CreatedAt: at(1)}},
			View{Status: StatusPending},
		},
		{
			"ai marker comment is not a human turn",
			[]Comment{ai(1, "補足です")},
			View{Status: StatusPending},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Derive(tt.comments, responder)
			if got != tt.want {
				t.Errorf("Derive() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBuildIssueBody(t *testing.T) {
	body := BuildIssueBody(
		"今週の release で採用する方式を決めてほしい",
		"A は安い。B は運用が楽。",
		[]string{"A", "B"},
		"neguse/someproject @ main",
	)
	for _, want := range []string{
		"> from: neguse/someproject @ main",
		"今週の release で採用する方式を決めてほしい",
		"## Context",
		"A は安い。B は運用が楽。",
		"## Choices",
		"A / B",
		"more:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("BuildIssueBody() missing %q in:\n%s", want, body)
		}
	}

	minimal := BuildIssueBody("短い質問", "", nil, "")
	for _, notWant := range []string{"from:", "## Context", "## Choices"} {
		if strings.Contains(minimal, notWant) {
			t.Errorf("minimal body should not contain %q:\n%s", notWant, minimal)
		}
	}
}
