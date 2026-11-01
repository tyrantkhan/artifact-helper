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
	"github.com/tyrantkhan/claude-artifact-helper/skills"
)

// fakeEnv has every host present and records the host command lines it runs.
func fakeEnv(t *testing.T, missing ...string) (Env, *[]string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	var ran []string
	env := Env{Home: home, Self: "/opt/bin/claude-artifact-helper",
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

func TestInstallEveryHost(t *testing.T) {
	env, ran := fakeEnv(t)
	res, err := Install(context.Background(), env, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 {
		t.Fatalf("results = %+v", res)
	}
	for _, r := range res {
		if r.MCP != "added" {
			t.Errorf("%s: mcp = %s", r.Host, r.MCP)
		}
		b, err := os.ReadFile(r.Skill)
		if err != nil || string(b) != string(skills.ClaudeArtifactHelper) {
			t.Errorf("%s: skill at %s: %v", r.Host, r.Skill, err)
		}
	}
	want := []string{
		"claude mcp remove --scope user claude-artifact-helper",
		"claude mcp add --scope user claude-artifact-helper -- /opt/bin/claude-artifact-helper serve",
		"codex mcp remove claude-artifact-helper",
		"codex mcp add claude-artifact-helper -- /opt/bin/claude-artifact-helper serve",
		"gemini mcp remove --scope user claude-artifact-helper",
		"gemini mcp add --scope user claude-artifact-helper /opt/bin/claude-artifact-helper serve",
	}
	if strings.Join(*ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran:\n%s\nwant:\n%s", strings.Join(*ran, "\n"), strings.Join(want, "\n"))
	}
	for _, p := range []string{".claude/skills", ".agents/skills", ".cursor/skills", ".gemini/skills"} {
		if _, err := os.Stat(filepath.Join(env.Home, p, skills.Name, "SKILL.md")); err != nil {
			t.Errorf("%s: %v", p, err)
		}
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

func TestInstallSkipsHostsNotPresent(t *testing.T) {
	env, ran := fakeEnv(t, "codex", "cursor")
	res, err := Install(context.Background(), env, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		skipped := r.Host == "codex" || r.Host == "cursor"
		if skipped != strings.HasPrefix(r.MCP, "skipped: not found") || skipped != strings.HasPrefix(r.Skill, "skipped: not found") {
			t.Errorf("%s: %+v", r.Host, r)
		}
	}
	for _, line := range *ran {
		if strings.HasPrefix(line, "codex") {
			t.Errorf("ran %q for a host that is not present", line)
		}
	}
	// Named explicitly, a host is installed anyway.
	res, err = Install(context.Background(), env, Options{Hosts: []string{"codex"}})
	if err != nil || len(res) != 1 || res[0].MCP != "added" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestInstallParts(t *testing.T) {
	env, ran := fakeEnv(t)
	res, err := Install(context.Background(), env, Options{Hosts: []string{"claude"}, Skill: true})
	if err != nil || res[0].MCP != "skipped: --skill-only" || !strings.HasSuffix(res[0].Skill, "SKILL.md") || len(*ran) != 0 {
		t.Fatalf("res=%+v err=%v ran=%v", res, err, *ran)
	}
	res, err = Install(context.Background(), env, Options{Hosts: []string{"cursor"}, MCP: true})
	if err != nil || res[0].MCP != "added" || res[0].Skill != "skipped: --mcp-only" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".cursor", "skills")); err == nil {
		t.Fatal("--mcp-only wrote the skill")
	}
	_, err = Install(context.Background(), env, Options{Hosts: []string{"vim"}})
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
	if _, err := Install(context.Background(), env, Options{Hosts: []string{"cursor"}, MCP: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	if !strings.Contains(string(b), `"other"`) || !strings.Contains(string(b), `"theme": "dark"`) || !strings.Contains(string(b), `"claude-artifact-helper"`) {
		t.Fatalf("mcp.json = %s", b)
	}
	if _, err := Uninstall(context.Background(), env, Options{Hosts: []string{"cursor"}, MCP: true}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(f)
	if !strings.Contains(string(b), `"other"`) || strings.Contains(string(b), `"claude-artifact-helper"`) {
		t.Fatalf("mcp.json after uninstall = %s", b)
	}
	// A broken file is refused, not overwritten.
	if err := os.WriteFile(f, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Install(context.Background(), env, Options{Hosts: []string{"cursor"}, MCP: true})
	if r, ok := headless.IsRefusal(err); !ok || r.Code != "host-failed" {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "not json" {
		t.Fatalf("the broken file was overwritten: %s", b)
	}
}

func TestUninstall(t *testing.T) {
	env, ran := fakeEnv(t)
	if _, err := Install(context.Background(), env, Options{}); err != nil {
		t.Fatal(err)
	}
	*ran = nil
	res, err := Uninstall(context.Background(), env, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.MCP != "removed" {
			t.Errorf("%s: mcp = %s", r.Host, r.MCP)
		}
		if _, err := os.Stat(r.Skill); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: skill still at %s", r.Host, r.Skill)
		}
	}
	want := []string{
		"claude mcp remove --scope user claude-artifact-helper",
		"codex mcp remove claude-artifact-helper",
		"gemini mcp remove --scope user claude-artifact-helper",
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
	_, err := Install(context.Background(), env, Options{Hosts: []string{"codex"}})
	r, ok := headless.IsRefusal(err)
	if !ok || r.Code != "host-failed" || !strings.Contains(r.Message, "boom") || !strings.Contains(r.Fix, "codex mcp add") {
		t.Fatalf("err = %v", err)
	}
}
