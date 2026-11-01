// Package cli is the claude-artifact-helper command line: the MCP server, and the same publish as a one-shot
// command that prints one JSON object.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
	"github.com/tyrantkhan/claude-artifact-helper/internal/mcpserver"
)

// Version is this build's version. release-please bumps it; release builds also set it with -ldflags.
var Version = "0.1.0" // x-release-please-version

// Env is what the command line reads and writes, injected so tests can drive it.
type Env struct {
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
	Environ func() []string
	// Serve runs the MCP server on stdin and stdout. Nil uses the real stdio transport.
	Serve func(ctx context.Context, s *mcp.Server) error
}

type command struct {
	name    string
	summary string
	help    string
	run     func(env *Env, args []string) error
}

func commands() []command {
	return []command{
		{"serve", "Run the MCP server on stdin and stdout; this is what an MCP client launches", serveHelp, cmdServe},
		{"publish", "Publish one file as an artifact and print its link as JSON", publishHelp, cmdPublish},
		{"doctor", "Check that Claude Code is installed and signed in", doctorHelp, cmdDoctor},
	}
}

// Main returns the process exit code.
func Main(env *Env) int {
	if len(env.Args) == 0 || env.Args[0] == "help" || env.Args[0] == "-h" || env.Args[0] == "--help" {
		if len(env.Args) > 1 {
			for _, c := range commands() {
				if c.name == env.Args[1] {
					fmt.Fprint(env.Stdout, c.help)
					return 0
				}
			}
		}
		usage(env.Stdout)
		return 0
	}
	name := env.Args[0]
	if name == "version" || name == "--version" || name == "-v" {
		fmt.Fprintln(env.Stdout, "claude-artifact-helper "+Version)
		return 0
	}
	for _, c := range commands() {
		if c.name != name {
			continue
		}
		err := c.run(env, env.Args[1:])
		if err == nil || errors.Is(err, flag.ErrHelp) {
			return 0
		}
		writeError(env, name, err)
		return 1
	}
	fmt.Fprintf(env.Stderr, "claude-artifact-helper: unknown command %q\n\n", name)
	usage(env.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `claude-artifact-helper: publish a file as a claude.ai artifact through your Claude subscription, from any MCP
client or shell. Under the cover it runs Claude Code headlessly (claude -p) and lets its Artifact tool do the upload.

Usage: claude-artifact-helper <command> [flags]

`)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, `
Run claude-artifact-helper help <command> for its flags and result, and --version for this build's version.

Environment:
  CLAUDE_ARTIFACT_HELPER_CLAUDE          path of the claude binary (default: claude on PATH)
  CLAUDE_ARTIFACT_HELPER_MODEL           model for the headless run (default `+headless.DefaultModel+`)
  CLAUDE_ARTIFACT_HELPER_TIMEOUT         how long one publish may take (default `+headless.DefaultTimeout.String()+`)
  CLAUDE_ARTIFACT_HELPER_MAX_BUDGET_USD  cap passed to claude --max-budget-usd (default `+fmt.Sprint(headless.DefaultMaxBudgetUSD)+`; 0 for none)
  CLAUDE_ARTIFACT_HELPER_VERBOSE         set to 1 to log each step on stderr
  CLAUDE_CODE_OAUTH_TOKEN                a token from claude setup-token, for a machine without a claude login
`)
}

// writeError prints a refusal as JSON on stdout, the channel agents read, and anything else on stderr as well.
func writeError(env *Env, name string, err error) {
	rf, ok := headless.IsRefusal(err)
	if !ok {
		rf = &headless.Refusal{Code: "error", Message: err.Error()}
	}
	b, _ := json.Marshal(map[string]any{"ok": false, "command": name, "error": rf})
	fmt.Fprintln(env.Stdout, string(b))
	if rf.Code == "error" || name == "serve" {
		fmt.Fprintln(env.Stderr, "claude-artifact-helper "+name+": "+err.Error())
	}
}

func writeJSON(env *Env, name string, fields map[string]any) error {
	out := map[string]any{"ok": true, "command": name}
	for k, v := range fields {
		out[k] = v
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding result: %w", err)
	}
	_, err = fmt.Fprintln(env.Stdout, string(b))
	return err
}

func newFlags(env *Env, name, help string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, help) }
	return fs
}

// parse lets flags follow positional arguments, which the flag package alone stops at. "--" ends flags.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if i := len(args) - len(rest); i > 0 && args[i-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func badArgs(name, format string, args ...any) error {
	return &headless.Refusal{Code: "invalid-args", Message: fmt.Sprintf(format, args...), Fix: "run claude-artifact-helper help " + name}
}

// options reads the environment into headless options, with the flags a command may add on top.
func options(env *Env) (headless.Options, error) {
	opts, err := headless.OptionsFromEnv(env.Getenv)
	if err != nil {
		return opts, err
	}
	opts.Environ = env.Environ()
	if v := env.Getenv("CLAUDE_ARTIFACT_HELPER_VERBOSE"); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		opts.Log = env.Stderr
	}
	return opts, nil
}

func addOptionFlags(fs *flag.FlagSet, opts *headless.Options) {
	fs.StringVar(&opts.Claude, "claude", opts.Claude, "path of the claude binary")
	fs.StringVar(&opts.Model, "model", opts.Model, "model for the headless run")
	fs.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "how long one publish may take")
	fs.Float64Var(&opts.MaxBudgetUSD, "max-budget-usd", opts.MaxBudgetUSD, "cap passed to claude --max-budget-usd; 0 for the default, negative for none")
	fs.BoolFunc("verbose", "log each step on stderr", func(string) error { opts.Log = os.Stderr; return nil })
}

const serveHelp = `Usage: claude-artifact-helper serve [--claude <path>] [--model <name>] [--timeout <duration>] [--verbose]

Run the MCP server over stdio. An MCP client launches this command and talks JSON-RPC on its stdin and stdout;
logs go to stderr. The flags override the CLAUDE_ARTIFACT_HELPER_* environment variables.

Tools:
  publish_artifact  {"file_path": "<abs path>.html", "title"?, "description"?, "icon"?, "url"?}
                    -> {"url": "https://claude.ai/artifact/<id>", "updated": false, "turns": 3, "cost_usd": 0.1}
  check_setup       {} -> {"claude": "<path>", "version": "...", "loggedIn": true, "authMethod": "...", "model": "...", "timeout": "5m0s"}

A failed call returns isError with {"code", "message", "fix"} as its text.
`

func cmdServe(env *Env, args []string) error {
	opts, err := options(env)
	if err != nil {
		return err
	}
	fs := newFlags(env, "serve", serveHelp)
	addOptionFlags(fs, &opts)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return badArgs("serve", "serve takes no arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := mcpserver.New(opts, Version)
	fmt.Fprintf(env.Stderr, "claude-artifact-helper %s: MCP server on stdio (model %s)\n", Version, orDefault(opts.Model, headless.DefaultModel))
	serve := env.Serve
	if serve == nil {
		serve = func(ctx context.Context, s *mcp.Server) error { return s.Run(ctx, &mcp.StdioTransport{}) }
	}
	if err := serve(ctx, s); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("serving MCP: %w", err)
	}
	return nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

const publishHelp = `Usage: claude-artifact-helper publish <file> [--title <text>] [--description <text>] [--icon <word>] [--url <link>]
                                      [--claude <path>] [--model <name>] [--timeout <duration>] [--verbose]

Publish one .html (or .md) file as a claude.ai artifact and print its link. The same publish the MCP tool does,
for shells and agents that run commands instead of MCP tools.

--title is the fallback title when the page has no <title>; keep it the same across republishes.
--description is one sentence for the gallery card. --icon is one generic word for the browser-tab icon
(default page). --url republishes to an existing artifact instead of creating a new one.

Result:
  {"ok": true, "command": "publish", "url": "https://claude.ai/artifact/<id>", "path": "/abs/page.html",
   "updated": false, "model": "sonnet", "turns": 3, "costUSD": 0.11, "durationMs": 40210}

A refusal has "ok": false and an "error" with "code", "message" and "fix".
`

func cmdPublish(env *Env, args []string) error {
	opts, err := options(env)
	if err != nil {
		return err
	}
	fs := newFlags(env, "publish", publishHelp)
	var req headless.Request
	fs.StringVar(&req.Title, "title", "", "fallback title when the page has no <title>")
	fs.StringVar(&req.Description, "description", "", "one sentence for the gallery card")
	fs.StringVar(&req.Icon, "icon", "", "one generic word for the browser-tab icon (default page)")
	fs.StringVar(&req.URL, "url", "", "an existing artifact's link to update in place")
	addOptionFlags(fs, &opts)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return badArgs("publish", "pass exactly one file to publish")
	}
	req.Path = pos[0]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := headless.Publish(ctx, opts, req)
	if err != nil {
		return err
	}
	return writeJSON(env, "publish", map[string]any{
		"url": res.URL, "path": res.Path, "updated": res.Updated, "model": res.Model,
		"turns": res.Turns, "costUSD": res.CostUSD, "durationMs": res.DurationMS,
	})
}

const doctorHelp = `Usage: claude-artifact-helper doctor [--claude <path>] [--model <name>]

Check that the claude binary is found and signed in, and show the settings a publish would use.

Result:
  {"ok": true, "command": "doctor", "claude": "/usr/local/bin/claude", "version": "2.1.0 (Claude Code)",
   "loggedIn": true, "authMethod": "claude.ai", "model": "sonnet", "timeout": "5m0s"}

A refusal has "ok": false and an "error" with "code" (no-claude, not-logged-in, claude-failed), "message" and "fix".
`

func cmdDoctor(env *Env, args []string) error {
	opts, err := options(env)
	if err != nil {
		return err
	}
	fs := newFlags(env, "doctor", doctorHelp)
	addOptionFlags(fs, &opts)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return badArgs("doctor", "doctor takes no arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h, err := headless.Doctor(ctx, opts)
	if err != nil {
		return err
	}
	return writeJSON(env, "doctor", map[string]any{
		"claude": h.Claude, "version": h.Version, "loggedIn": h.LoggedIn, "authMethod": h.AuthMethod,
		"model": h.Model, "timeout": h.Timeout,
	})
}
