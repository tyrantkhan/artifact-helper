package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
)

// fakeEnv has every host present and records the host command lines it runs.
func fakeEnv(t *testing.T, missing ...string) (Env, *[]string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ran []string
	env := Env{Home: home, Self: "/opt/bin/artifact-helper",
		LookPath: func(name string) (string, error) {
			for _, m := range missing {
				if m == name {
					return "", errors.New("not found")
				}
			}
			return "/usr/bin/" + name, nil
		},
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			line := name + " " + strings.Join(args, " ")
			ran = append(ran, line)
			if strings.Contains(line, " remove") && !strings.Contains(line, "fail-remove") {
				return "No MCP server found", errors.New("exit 1")
			}
			return "ok", nil
		}}
	for _, m := range missing {
		if m == "cursor" {
			os.RemoveAll(filepath.Join(home, ".cursor"))
		}
	}
	return env, &ran
}

func TestAddServerOnEveryHost(t *testing.T) {
	env, ran := fakeEnv(t)
	res, err := AddServer(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 {
		t.Fatalf("results = %+v", res)
	}
	for _, r := range res {
		if r.Server != "added" {
			t.Errorf("%s: server = %s", r.Host, r.Server)
		}
	}
	want := []string{
		"claude mcp remove --scope user artifact-helper",
		"claude mcp add --scope user artifact-helper -- /opt/bin/artifact-helper serve",
		"codex mcp remove artifact-helper",
		"codex mcp add artifact-helper -- /opt/bin/artifact-helper serve",
		"gemini mcp remove --scope user artifact-helper",
		"gemini mcp add --scope user artifact-helper /opt/bin/artifact-helper serve",
	}
	if strings.Join(*ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran:\n%s\nwant:\n%s", strings.Join(*ran, "\n"), strings.Join(want, "\n"))
	}
	b, _ := os.ReadFile(filepath.Join(env.Home, ".cursor", "mcp.json"))
	var cfg struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.MCPServers[ServerName].Command != env.Self || strings.Join(cfg.MCPServers[ServerName].Args, "") != "serve" {
		t.Fatalf("cursor mcp.json = %s (%v)", b, err)
	}
}

func TestAddServerSkipsHostsNotPresent(t *testing.T) {
	env, ran := fakeEnv(t, "codex", "cursor")
	res, err := AddServer(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		skipped := r.Host == "codex" || r.Host == "cursor"
		if skipped != strings.HasPrefix(r.Server, "skipped: not found") {
			t.Errorf("%s: %+v", r.Host, r)
		}
	}
	for _, line := range *ran {
		if strings.HasPrefix(line, "codex") {
			t.Errorf("ran %q for a host that is not present", line)
		}
	}
	// Named explicitly, a host is set up anyway.
	res, err = AddServer(context.Background(), env, []string{"codex"})
	if err != nil || len(res) != 1 || res[0].Server != "added" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	_, err = AddServer(context.Background(), env, []string{"vim"})
	if r, ok := headless.IsRefusal(err); !ok || r.Code != "invalid-args" {
		t.Fatalf("err = %v", err)
	}
}

func TestCursorConfigKeepsOtherServers(t *testing.T) {
	env, _ := fakeEnv(t)
	f := filepath.Join(env.Home, ".cursor", "mcp.json")
	if err := os.WriteFile(f, []byte(`{"mcpServers":{"other":{"command":"x"}},"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddServer(context.Background(), env, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	if !strings.Contains(string(b), `"other"`) || !strings.Contains(string(b), `"theme": "dark"`) || !strings.Contains(string(b), `"artifact-helper"`) {
		t.Fatalf("mcp.json = %s", b)
	}
	if _, err := RemoveServer(context.Background(), env, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(f)
	if !strings.Contains(string(b), `"other"`) || strings.Contains(string(b), `"artifact-helper"`) {
		t.Fatalf("mcp.json after uninstall = %s", b)
	}
	// A broken file is refused, not overwritten.
	if err := os.WriteFile(f, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := AddServer(context.Background(), env, []string{"cursor"})
	if r, ok := headless.IsRefusal(err); !ok || r.Code != "host-failed" {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "not json" {
		t.Fatalf("the broken file was overwritten: %s", b)
	}
}

func TestRemoveServer(t *testing.T) {
	env, ran := fakeEnv(t)
	if _, err := AddServer(context.Background(), env, nil); err != nil {
		t.Fatal(err)
	}
	*ran = nil
	res, err := RemoveServer(context.Background(), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Server != "removed" {
			t.Errorf("%s: server = %s", r.Host, r.Server)
		}
	}
	want := []string{
		"claude mcp remove --scope user artifact-helper",
		"codex mcp remove artifact-helper",
		"gemini mcp remove --scope user artifact-helper",
	}
	if strings.Join(*ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran:\n%s", strings.Join(*ran, "\n"))
	}
}

func TestHostCommandFailureIsARefusal(t *testing.T) {
	env, _ := fakeEnv(t)
	env.Run = func(_ context.Context, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), " add") || args[1] == "add" {
			return "boom", errors.New("exit 2")
		}
		return "", nil
	}
	_, err := AddServer(context.Background(), env, []string{"codex"})
	r, ok := headless.IsRefusal(err)
	if !ok || r.Code != "host-failed" || !strings.Contains(r.Message, "boom") || !strings.Contains(r.Fix, "codex mcp add") {
		t.Fatalf("err = %v", err)
	}
}
