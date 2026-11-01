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

func fakeClaude(t *testing.T, stdout string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo '9.9.9'; exit 0;; auth) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\n" +
		"cat <<'STDOUT'\n" + stdout + "\nSTDOUT\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func run(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	e := &Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return env[k] }, Environ: func() []string { return nil }}
	return Main(e), out.String(), errb.String()
}

func TestHelpAndVersion(t *testing.T) {
	code, out, _ := run(t, nil)
	if code != 0 || !strings.Contains(out, "Usage: claude-artifact-helper <command>") || !strings.Contains(out, "CLAUDE_ARTIFACT_HELPER_MODEL") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = run(t, nil, "help", "publish")
	if code != 0 || !strings.HasPrefix(out, "Usage: claude-artifact-helper publish") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = run(t, nil, "--version")
	if code != 0 || out != "claude-artifact-helper "+Version+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, _, errs := run(t, nil, "bogus")
	if code != 2 || !strings.Contains(errs, `unknown command "bogus"`) {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestPublishPrintsJSON(t *testing.T) {
	bin := fakeClaude(t, `{"type":"result","subtype":"success","num_turns":3,"total_cost_usd":0.1,"duration_ms":10,"structured_output":{"url":"https://claude.ai/artifact/Abc123","error":""}}`)
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin, "CLAUDE_ARTIFACT_HELPER_MODEL": "haiku"}, "publish", p, "--title", "x")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var res struct {
		OK      bool
		Command string
		URL     string
		Path    string
		Model   string
		Turns   int
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Command != "publish" || res.URL != "https://claude.ai/artifact/Abc123" || res.Path != p || res.Model != "haiku" || res.Turns != 3 {
		t.Fatalf("result = %+v", res)
	}
	// The --model flag wins over the environment.
	code, out, _ = run(t, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin, "CLAUDE_ARTIFACT_HELPER_MODEL": "haiku"}, "publish", "--model", "opus", p)
	if code != 0 || !strings.Contains(out, `"model": "opus"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestPublishRefusalsAreJSONOnStdout(t *testing.T) {
	bin := fakeClaude(t, "")
	cases := []struct {
		args []string
		env  map[string]string
		code string
	}{
		{[]string{"publish"}, nil, "invalid-args"},
		{[]string{"publish", "a.html", "b.html"}, nil, "invalid-args"},
		{[]string{"publish", "/no/such.html"}, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin}, "invalid-file"},
		{[]string{"publish", "/no/such.html"}, map[string]string{"CLAUDE_ARTIFACT_HELPER_TIMEOUT": "never"}, "invalid-env"},
		{[]string{"doctor"}, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": "/no/such/claude"}, "no-claude"},
		{[]string{"doctor", "extra"}, nil, "invalid-args"},
		{[]string{"serve", "extra"}, nil, "invalid-args"},
	}
	for _, c := range cases {
		code, out, _ := run(t, c.env, c.args...)
		var res struct {
			OK    bool
			Error struct{ Code, Fix string }
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("%v: %q: %v", c.args, out, err)
		}
		if code != 1 || res.OK || res.Error.Code != c.code || res.Error.Fix == "" {
			t.Errorf("%v: code=%d result=%+v", c.args, code, res)
		}
	}
}

func TestDoctorPrintsJSON(t *testing.T) {
	bin := fakeClaude(t, "")
	code, out, _ := run(t, map[string]string{"CLAUDE_ARTIFACT_HELPER_CLAUDE": bin}, "doctor")
	if code != 0 || !strings.Contains(out, `"loggedIn": true`) || !strings.Contains(out, `"version": "9.9.9"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestServeRunsTheMCPServer(t *testing.T) {
	bin := fakeClaude(t, "")
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
