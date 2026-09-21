// Command ask lets an AI ask a human a question asynchronously, via issues
// in a dedicated private GitHub inbox repository. See docs/PRD.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/neguse/ask/internal/ask"
)

const usage = `usage:
  ask init --inbox OWNER/REPO --responder LOGIN [--timeout 10m]
  ask create --title TITLE [--question Q] [--context C] [--choice X]...
             [--json] [--wait] [--timeout 8m]
  ask "QUESTION" [--context C] [--choice X]... [--json] [--wait] [--timeout 8m]
  ask detail ID TEXT
  ask wait ID [--timeout 8m] [--json]
  ask show ID [--json]
  ask list [--json]

Statuses: pending, needs_detail, answered, timeout.
Exit code is 0 for every derived status including timeout, 1 on errors,
2 on usage errors.`

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches to the subcommands in internal/ask. The bare-question
// shorthand treats a first argument that is not a known subcommand as
// `create --title QUESTION`.
func run(args []string) int {
	if len(args) == 0 {
		return usageError()
	}

	var err error
	switch args[0] {
	case "init":
		err = runInit(args[1:])
	case "create":
		err = runCreate(args[1:])
	case "detail":
		err = runDetail(args[1:])
	case "wait":
		err = runWait(args[1:])
	case "show":
		err = runShow(args[1:])
	case "list":
		err = runList(args[1:])
	default:
		if args[0] != "-" && strings.HasPrefix(args[0], "-") {
			return usageError()
		}
		err = runShorthand(args[0], args[1:])
	}

	if err == nil {
		return 0
	}
	if err == errUsage {
		return usageError()
	}
	fmt.Fprintf(os.Stderr, "ask: %v\n", err)
	return 1
}

var errUsage = fmt.Errorf("invalid command usage")

type stringListFlag []string

func (v *stringListFlag) String() string {
	return strings.Join(*v, ",")
}

func (v *stringListFlag) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

func usageError() int {
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

func runInit(args []string) error {
	fs := newFlagSet("init")
	inbox := fs.String("inbox", "", "inbox repository")
	responder := fs.String("responder", "", "GitHub login to notify")
	timeout := fs.Duration("timeout", 10*time.Minute, "verification timeout")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *inbox == "" || *responder == "" || *timeout < 0 {
		return errUsage
	}
	return ask.CmdInit(ask.ExecRunner{}, os.Stdout, *inbox, *responder, *timeout)
}

func runCreate(args []string) error {
	fs := newFlagSet("create")
	in := ask.CreateInput{
		Timeout: 8 * time.Minute,
	}
	var choices stringListFlag
	fs.StringVar(&in.Title, "title", "", "issue title")
	fs.StringVar(&in.Question, "question", "", "question text")
	fs.Var(&choices, "choice", "answer choice (repeatable)")
	addCreateFlags(fs, &in)
	if fs.Parse(args) != nil || fs.NArg() != 0 || strings.TrimSpace(in.Title) == "" || in.Timeout < 0 {
		return errUsage
	}
	if in.Question == "" {
		in.Question = in.Title
	}
	in.Choices = []string(choices)

	cfg, err := ask.LoadConfig()
	if err != nil {
		return err
	}
	return ask.CmdCreate(ask.ExecRunner{}, os.Stdout, cfg, in)
}

func runShorthand(question string, args []string) error {
	in := ask.CreateInput{
		Title:    question,
		Question: question,
		Timeout:  8 * time.Minute,
	}
	fs := newFlagSet("create")
	var choices stringListFlag
	fs.Var(&choices, "choice", "answer choice (repeatable)")
	addCreateFlags(fs, &in)
	if fs.Parse(args) != nil || fs.NArg() != 0 || strings.TrimSpace(question) == "" || in.Timeout < 0 {
		return errUsage
	}
	in.Choices = []string(choices)
	cfg, err := ask.LoadConfig()
	if err != nil {
		return err
	}
	return ask.CmdCreate(ask.ExecRunner{}, os.Stdout, cfg, in)
}

func addCreateFlags(fs *flag.FlagSet, in *ask.CreateInput) {
	fs.StringVar(&in.Context, "context", "", "question context")
	fs.BoolVar(&in.JSON, "json", false, "write JSON")
	fs.BoolVar(&in.Wait, "wait", false, "wait for a reply")
	fs.DurationVar(&in.Timeout, "timeout", in.Timeout, "wait timeout")
}

func runDetail(args []string) error {
	fs := newFlagSet("detail")
	if fs.Parse(args) != nil || fs.NArg() != 2 || strings.TrimSpace(fs.Arg(1)) == "" {
		return errUsage
	}
	id, err := positiveID(fs.Arg(0))
	if err != nil {
		return errUsage
	}
	cfg, err := ask.LoadConfig()
	if err != nil {
		return err
	}
	return ask.CmdDetail(ask.ExecRunner{}, os.Stdout, cfg, id, fs.Arg(1))
}

func runWait(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	id, err := positiveID(args[0])
	if err != nil {
		return errUsage
	}
	fs := newFlagSet("wait")
	timeout := fs.Duration("timeout", 8*time.Minute, "wait timeout")
	jsonOut := fs.Bool("json", false, "write JSON")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *timeout < 0 {
		return errUsage
	}
	cfg, err := ask.LoadConfig()
	if err != nil {
		return err
	}
	return ask.CmdWait(ask.ExecRunner{}, os.Stdout, cfg, id, *timeout, *jsonOut)
}

func runShow(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	id, err := positiveID(args[0])
	if err != nil {
		return errUsage
	}
	fs := newFlagSet("show")
	jsonOut := fs.Bool("json", false, "write JSON")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return errUsage
	}
	cfg, err := ask.LoadConfig()
	if err != nil {
		return err
	}
	return ask.CmdShow(ask.ExecRunner{}, os.Stdout, cfg, id, *jsonOut)
}

func runList(args []string) error {
	fs := newFlagSet("list")
	jsonOut := fs.Bool("json", false, "write JSON")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return errUsage
	}
	cfg, err := ask.LoadConfig()
	if err != nil {
		return err
	}
	return ask.CmdList(ask.ExecRunner{}, os.Stdout, cfg, *jsonOut)
}

func positiveID(value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, errUsage
	}
	return id, nil
}
