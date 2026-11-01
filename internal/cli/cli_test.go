package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	link       = "https://claude.ai/artifact/Abc123"
	threadUUID = "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b"
	linkOK     = `{"type":"result","subtype":"success","num_turns":3,"total_cost_usd":0.1,"duration_ms":10,"structured_output":{"url":"` + link + `","error":""}}`
)

func fakeClaude(t *testing.T, outputs map[string]string, pre string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	var b strings.Builder
	b.WriteString("#!/bin/sh\ncase \"$1\" in --version) echo '9.9.9'; exit 0;; auth) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\n")
	b.WriteString("for a; do prompt=$a; done\n" + pre + "\ncase \"$prompt\" in\n")
	for op, out := range outputs {
		if op != "" {
			b.WriteString("  'Task: " + op + ".'*) cat <<'STDOUT'\n" + out + "\nSTDOUT\n;;\n")
		}
	}
	b.WriteString("  *) cat <<'STDOUT'\n" + outputs[""] + "\nSTDOUT\n;;\nesac\n")
	if err := os.WriteFile(bin, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func run(t *testing.T, env map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	e := &Env{Args: args, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return env[k] }, Environ: func() []string { return nil }}
	return Main(e), out.String(), errb.String()
}

func result(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	return m
}

func TestHelpAndVersion(t *testing.T) {
	code, out, _ := run(t, nil, "")
	if code != 0 || !strings.Contains(out, "Usage: claude-artifact-helper <command>") || !strings.Contains(out, "CLAUDE_ARTIFACT_HELPER_MODEL") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	for _, c := range commands() {
		code, out, _ = run(t, nil, "", "help", c.name)
		if code != 0 || !strings.HasPrefix(out, "Usage: claude-artifact-helper "+c.name) {
			t.Fatalf("help %s: code=%d out=%q", c.name, code, out)
		}
	}
	code, out, _ = run(t, nil, "", "--version")
	if code != 0 || out != "claude-artifact-helper "+Version+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, _, errs := run(t, nil, "", "bogus")
	if code != 2 || !strings.Contains(errs, `unknown command "bogus"`) {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestPublishPrintsJSON(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"publish": linkOK}, "")
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin, "CLAUDE_ARTIFACT_HELPER_MODEL": "haiku"}
	code, out, _ := run(t, env, "", "publish", p, "--title", "x")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	res := result(t, out)
	if res["ok"] != true || res["command"] != "publish" || res["url"] != link || res["path"] != p || res["model"] != "haiku" || res["turns"] != 3.0 {
		t.Fatalf("result = %+v", res)
	}
	// The --model flag wins over the environment.
	code, out, _ = run(t, env, "", "publish", "--model", "opus", p)
	if code != 0 || !strings.Contains(out, `"model": "opus"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestReadEditCommentsReplyResolve(t *testing.T) {
	readOK := `{"type":"result","subtype":"success","num_turns":2,"structured_output":{"path":"index.html","error":""}}`
	listing := `{"type":"result","subtype":"success","structured_output":{"threads":[{"id":"` + threadUUID + `","author":"Sam","text":"Typo","resolved":false,"activated":true,"replies":[]}],"raw":"1 thread","more_cursor":"","error":""}}`
	threadOK := `{"type":"result","subtype":"success","structured_output":{"replied":true,"resolved":true,"message":"posted","error":""}}`
	bin := fakeClaude(t, map[string]string{"read": readOK, "publish": linkOK, "comments": listing, "reply": threadOK, "resolve": threadOK},
		`[ -e "$PWD/index.html" ] || printf '<h1>Hello</h1>' > "$PWD/index.html"`)
	env := map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin}
	out := t.TempDir()

	code, o, _ := run(t, env, "", "read", link, "--out", out)
	res := result(t, o)
	saved := filepath.Join(out, "index.html")
	if code != 0 || res["command"] != "read" || res["savedPath"] != saved || res["path"] != "index.html" {
		t.Fatalf("exit %d: %s", code, o)
	}

	code, o, _ = run(t, env, `[{"old_str":"Hello","new_str":"Hi"}]`, "edit", link, "--file", saved)
	res = result(t, o)
	if code != 0 || res["applied"] != 1.0 || res["fetched"] != false || res["path"] != saved {
		t.Fatalf("exit %d: %s", code, o)
	}
	if b, _ := os.ReadFile(saved); string(b) != "<h1>Hi</h1>" {
		t.Fatalf("file = %s", b)
	}
	from := filepath.Join(t.TempDir(), "edits.json")
	if err := os.WriteFile(from, []byte(`[{"old_str":"Hi","new_str":"Hey"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, o, _ = run(t, env, "", "edit", link, "--file", saved, "--from", from)
	if code != 0 || result(t, o)["applied"] != 1.0 {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, `not json`, "edit", link, "--file", saved)
	if code != 1 || result(t, o)["error"].(map[string]any)["code"] != "invalid-input" {
		t.Fatalf("exit %d: %s", code, o)
	}

	code, o, _ = run(t, env, "", "comments", link)
	res = result(t, o)
	if code != 0 || res["raw"] != "1 thread" || len(res["threads"].([]any)) != 1 {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, "", "reply", link, threadUUID, "--text", "Fixed", "--resolve")
	res = result(t, o)
	if code != 0 || res["replied"] != true || res["resolved"] != true || res["threadId"] != threadUUID {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, "", "resolve", link, threadUUID)
	if code != 0 || result(t, o)["resolved"] != true {
		t.Fatalf("exit %d: %s", code, o)
	}
}

func TestRefusalsAreJSONOnStdout(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"": ""}, "")
	withBin := map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin}
	cases := []struct {
		args []string
		env  map[string]string
		code string
	}{
		{[]string{"publish"}, nil, "invalid-args"},
		{[]string{"publish", "a.html", "b.html"}, nil, "invalid-args"},
		{[]string{"publish", "/no/such.html"}, withBin, "invalid-file"},
		{[]string{"publish", "/no/such.html"}, map[string]string{"CLAUDE_ARTIFACT_HELPER_TIMEOUT": "never"}, "invalid-env"},
		{[]string{"read", "https://example.com/x"}, withBin, "invalid-url"},
		{[]string{"edit", link, "--from", "/no/such.json"}, withBin, "invalid-input"},
		{[]string{"comments"}, withBin, "invalid-args"},
		{[]string{"reply", link}, withBin, "invalid-args"},
		{[]string{"reply", link, threadUUID}, withBin, "invalid-args"},
		{[]string{"resolve", link, "nope"}, withBin, "invalid-thread"},
		{[]string{"doctor"}, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": "/no/such/claude"}, "no-claude"},
		{[]string{"doctor", "extra"}, nil, "invalid-args"},
		{[]string{"serve", "extra"}, nil, "invalid-args"},
	}
	for _, c := range cases {
		code, out, _ := run(t, c.env, "", c.args...)
		res := result(t, out)
		e, _ := res["error"].(map[string]any)
		if code != 1 || res["ok"] != false || e["code"] != c.code || e["fix"] == "" {
			t.Errorf("%v: code=%d result=%+v", c.args, code, res)
		}
	}
}

func TestDoctorPrintsJSON(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"": ""}, "")
	code, out, _ := run(t, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin}, "", "doctor")
	if code != 0 || !strings.Contains(out, `"loggedIn": true`) || !strings.Contains(out, `"version": "9.9.9"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestServeRunsTheMCPServer(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"": ""}, "")
	var served *mcp.Server
	var errb bytes.Buffer
	e := &Env{Args: []string{"serve", "--model", "opus"}, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &errb,
		Getenv: func(k string) string {
			if k == "CLAUDE_ARTIFACT_HELPER_CLAUDE" {
				return bin
			}
			return ""
		},
		Environ: func() []string { return nil },
		Serve: func(ctx context.Context, s *mcp.Server) error {
			served = s
			t1, t2 := mcp.NewInMemoryTransports()
			if _, err := s.Connect(ctx, t1, nil); err != nil {
				return err
			}
			sess, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, t2, nil)
			if err != nil {
				return err
			}
			defer sess.Close()
			res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "check_setup", Arguments: map[string]any{}})
			if err != nil {
				return err
			}
			b, _ := json.Marshal(res.StructuredContent)
			if !strings.Contains(string(b), `"model":"opus"`) {
				t.Errorf("structured = %s", b)
			}
			return nil
		}}
	if code := Main(e); code != 0 || served == nil {
		t.Fatalf("exit %d, served=%v, stderr=%s", code, served != nil, errb.String())
	}
	if !strings.Contains(errb.String(), "MCP server on stdio (model opus)") {
		t.Fatalf("stderr = %q", errb.String())
	}
}
