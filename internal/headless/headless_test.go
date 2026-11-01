package headless

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes a shell script that stands in for the claude binary. It records its arguments and working
// directory to a file and prints the given stdout, exiting with code.
func fakeClaude(t *testing.T, stdout string, code int) (bin, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"{ printf '%s\\0' \"$PWD\" \"$@\"; } > " + record + "\n" +
		"case \"$1\" in --version) echo '9.9.9 (Claude Code)'; exit 0;; auth) echo '" + authStatus + "'; exit 0;; esac\n" +
		"cat <<'STDOUT'\n" + stdout + "\nSTDOUT\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

var authStatus = `{"loggedIn": true, "authMethod": "claude.ai"}`

func page(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("<!doctype html><title>T</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPublishRunsClaudeInTheFilesDirectory(t *testing.T) {
	bin, record := fakeClaude(t, `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.12,"duration_ms":4000,"result":"done","structured_output":{"url":"https://claude.ai/artifact/Abc123","error":""}}`, 0)
	p := page(t, "page.html")
	res, err := Publish(context.Background(), Options{Claude: bin, Model: "haiku", Timeout: time.Minute}, Request{Path: p, Title: "My Page", Icon: "chart", Description: "A test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://claude.ai/artifact/Abc123" || res.Updated || res.Turns != 3 || res.CostUSD != 0.12 || res.DurationMS != 4000 || res.Model != "haiku" || res.Path != p {
		t.Fatalf("result = %+v", res)
	}
	rec, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(rec), "\x00"), "\x00")
	if got, want := strings.TrimSpace(lines[0]), filepath.Dir(p); got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
	args := lines[1:]
	for _, want := range []string{"-p", "--output-format", "json", "--model", "haiku", "--tools", "Artifact,Read", "--permission-prompts", "none", "--no-session-persistence", "--max-budget-usd", "5"} {
		if !contains(args, want) {
			t.Errorf("args lack %q: %v", want, args)
		}
	}
	prompt := args[len(args)-1]
	for _, want := range []string{`"page.html"`, `"file_path":"page.html"`, `"icon":"chart"`, `"title":"My Page"`, `"description":"A test"`, "Read the file"} {
		if !strings.Contains(prompt, want) && !strings.Contains(prompt, strings.ReplaceAll(want, "Read the file", "Read \"page.html\"")) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, `"url":`) {
		t.Errorf("a new artifact's prompt names a url:\n%s", prompt)
	}
}

func TestPublishUpdatesWhenURLGiven(t *testing.T) {
	bin, record := fakeClaude(t, `{"type":"result","subtype":"success","is_error":false,"structured_output":{"url":"https://claude.ai/artifact/Abc123","error":""}}`, 0)
	res, err := Publish(context.Background(), Options{Claude: bin}, Request{Path: page(t, "p.md"), URL: "https://claude.ai/artifact/Abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Model != DefaultModel {
		t.Fatalf("result = %+v", res)
	}
	rec, _ := os.ReadFile(record)
	s := string(rec)
	if !strings.Contains(s, `action "read" and url "https://claude.ai/artifact/Abc123"`) || !strings.Contains(s, `"url":"https://claude.ai/artifact/Abc123"`) {
		t.Fatalf("update prompt lacks the read step or the url argument:\n%s", s)
	}
	if strings.Contains(s, `"icon"`) {
		t.Fatalf("an update without --icon must not set one:\n%s", s)
	}
}

func TestPublishRefusals(t *testing.T) {
	ok := `{"type":"result","subtype":"success","is_error":false,"structured_output":{"url":"https://claude.ai/artifact/Abc123","error":""}}`
	cases := []struct {
		name, stdout string
		code         int
		req          Request
		opts         Options
		want         string
	}{
		{name: "missing file", stdout: ok, req: Request{Path: "/no/such.html"}, want: "invalid-file"},
		{name: "wrong extension", stdout: ok, req: Request{Path: "self"}, want: "invalid-file"},
		{name: "bad url", stdout: ok, req: Request{URL: "https://example.com/x"}, want: "invalid-url"},
		{name: "no claude", stdout: ok, opts: Options{Claude: "/no/such/claude"}, want: "no-claude"},
		{name: "tool refused", stdout: `{"type":"result","subtype":"success","is_error":false,"result":"no","structured_output":{"url":"","error":"the file is too large"}}`, want: "publish-refused"},
		{name: "run errored", stdout: `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"Reached max turns"}`, want: "publish-failed"},
		{name: "not logged in", stdout: `{"type":"result","subtype":"success","is_error":true,"result":"Authentication error · please log in"}`, want: "not-logged-in"},
		{name: "login text on stderr only", stdout: "Not logged in. Run claude auth login.", code: 1, want: "not-logged-in"},
		{name: "crash", stdout: "segfault", code: 2, want: "claude-failed"},
		{name: "no json", stdout: "hello", want: "bad-output"},
		{name: "odd url", stdout: `{"type":"result","subtype":"success","structured_output":{"url":"https://evil.example/x","error":""}}`, want: "bad-output"},
		{name: "link in plain reply", stdout: `{"type":"result","subtype":"success","result":"Done: https://claude.ai/artifact/Zz9 ok"}`, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, _ := fakeClaude(t, c.stdout, c.code)
			opts := c.opts
			if opts.Claude == "" {
				opts.Claude = bin
			}
			req := c.req
			switch req.Path {
			case "":
				req.Path = page(t, "p.html")
			case "self":
				req.Path = bin
			}
			res, err := Publish(context.Background(), opts, req)
			if c.want == "" {
				if err != nil || res.URL != "https://claude.ai/artifact/Zz9" {
					t.Fatalf("res=%+v err=%v", res, err)
				}
				return
			}
			r, ok := IsRefusal(err)
			if !ok || r.Code != c.want {
				t.Fatalf("err = %v, want code %s", err, c.want)
			}
			if r.Fix == "" {
				t.Errorf("refusal %s has no fix", r.Code)
			}
		})
	}
}

func TestPublishTimesOut(t *testing.T) {
	bin, _ := fakeClaude(t, "", 0)
	// Replace the body with a sleep; the record header stays.
	script, _ := os.ReadFile(bin)
	s := strings.Replace(string(script), "cat <<'STDOUT'", "exec sleep 5\ncat <<'STDOUT'", 1)
	if err := os.WriteFile(bin, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := Publish(context.Background(), Options{Claude: bin, Timeout: 300 * time.Millisecond}, Request{Path: page(t, "p.html")})
	r, ok := IsRefusal(err)
	if !ok || r.Code != "timeout" {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %s, the child was not stopped", time.Since(start))
	}
}

func TestOptionsFromEnv(t *testing.T) {
	env := map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": "/x/claude", "CLAUDE_ARTIFACT_HELPER_MODEL": "opus",
		"CLAUDE_ARTIFACT_HELPER_TIMEOUT": "90s", "CLAUDE_ARTIFACT_HELPER_MAX_BUDGET_USD": "0"}
	o, err := OptionsFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if o.Claude != "/x/claude" || o.Model != "opus" || o.Timeout != 90*time.Second || o.budget() > 0 {
		t.Fatalf("options = %+v", o)
	}
	args := Args(o, Request{}, "p.html")
	if contains(args, "--max-budget-usd") {
		t.Fatalf("a zero budget must pass no cap: %v", args)
	}
	for k, v := range map[string]string{"CLAUDE_ARTIFACT_HELPER_TIMEOUT": "soon", "CLAUDE_ARTIFACT_HELPER_MAX_BUDGET_USD": "-3"} {
		_, err := OptionsFromEnv(func(kk string) string {
			if kk == k {
				return v
			}
			return ""
		})
		if r, ok := IsRefusal(err); !ok || r.Code != "invalid-env" {
			t.Errorf("%s=%s: err = %v", k, v, err)
		}
	}
	o, _ = OptionsFromEnv(func(string) string { return "" })
	if o.model() != DefaultModel || o.timeout() != DefaultTimeout || o.budget() != DefaultMaxBudgetUSD {
		t.Fatalf("defaults = %+v", o)
	}
}

func TestDoctor(t *testing.T) {
	bin, _ := fakeClaude(t, "", 0)
	h, err := Doctor(context.Background(), Options{Claude: bin})
	if err != nil {
		t.Fatal(err)
	}
	if !h.LoggedIn || h.AuthMethod != "claude.ai" || h.Version != "9.9.9 (Claude Code)" || h.Claude != bin {
		t.Fatalf("health = %+v", h)
	}
	authStatus = `{"loggedIn": false}`
	t.Cleanup(func() { authStatus = `{"loggedIn": true, "authMethod": "claude.ai"}` })
	bin, _ = fakeClaude(t, "", 0)
	h, err = Doctor(context.Background(), Options{Claude: bin})
	if r, ok := IsRefusal(err); !ok || r.Code != "not-logged-in" || h == nil || h.LoggedIn {
		t.Fatalf("health = %+v, err = %v", h, err)
	}
	if _, err := Doctor(context.Background(), Options{Claude: "/no/such/claude"}); err == nil {
		t.Fatal("a missing binary passed")
	}
}

func TestValidURL(t *testing.T) {
	for s, want := range map[string]bool{
		"https://claude.ai/artifact/Abc_12-3": true, "https://claude.ai/code/artifact/0f1e2d3c": true,
		"http://claude.ai/artifact/x": false, "https://claude.ai/chat/x": false, "https://claude.ai/artifact/": false,
		"https://claude.ai.evil.com/artifact/x": false,
	} {
		if ValidURL(s) != want {
			t.Errorf("ValidURL(%q) = %v", s, !want)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
