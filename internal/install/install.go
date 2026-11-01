// Package install registers the MCP server with each agent host, and unregisters it. Hosts with a command
// line that manages MCP servers (Claude Code, Codex, Gemini CLI) are driven through it, so their configuration
// formats stay theirs; Cursor's mcp.json is edited directly. Skills are the skillinstall package's job.
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
)

// ServerName is the name the MCP server is registered under.
const ServerName = "artifact-helper"

// Env is what the installer reads and runs, injected so tests can drive it.
type Env struct {
	// Home is the user's home directory.
	Home string
	// Self is the absolute path of this binary, registered as the MCP server's command.
	Self string
	// LookPath finds a host's command line.
	LookPath func(string) (string, error)
	// Run runs a host's command line and returns its combined output.
	Run func(ctx context.Context, name string, args ...string) (string, error)
}

// Hosts lists the hosts in the order they are installed.
var Hosts = []string{"claude", "codex", "cursor", "gemini"}

// HostResult reports what happened on one host.
type HostResult struct {
	Host string `json:"host"`
	// Server says what happened to the registration: added, removed, or skipped with a reason.
	Server string `json:"server"`
}

type host struct {
	name string
	// present reports whether the host is on this machine; a host named explicitly is installed anyway.
	present func(env Env) bool
	// add registers the MCP server; remove unregisters it.
	add    func(ctx context.Context, env Env) error
	remove func(ctx context.Context, env Env) error
}

func hosts() map[string]host {
	onPath := func(cmd string) func(Env) bool {
		return func(env Env) bool { _, err := env.LookPath(cmd); return err == nil }
	}
	cli := func(cmd string, addArgs, removeArgs func(self string) []string) (func(context.Context, Env) error, func(context.Context, Env) error) {
		add := func(ctx context.Context, env Env) error {
			// Re-registering after a move of the binary: drop the old entry first, whatever its state.
			_, _ = env.Run(ctx, cmd, removeArgs(env.Self)...)
			out, err := env.Run(ctx, cmd, addArgs(env.Self)...)
			if err != nil {
				return &headless.Refusal{Code: "host-failed", Message: fmt.Sprintf("%s %s failed (%v): %s", cmd, strings.Join(addArgs(env.Self), " "), err, tail(out)),
					Fix: "fix what the message names, or add the server by hand: " + cmd + " " + strings.Join(addArgs(env.Self), " ")}
			}
			return nil
		}
		remove := func(ctx context.Context, env Env) error {
			out, err := env.Run(ctx, cmd, removeArgs(env.Self)...)
			if err != nil && !strings.Contains(strings.ToLower(out), "not found") && !strings.Contains(strings.ToLower(out), "no mcp server") {
				return &headless.Refusal{Code: "host-failed", Message: fmt.Sprintf("%s %s failed (%v): %s", cmd, strings.Join(removeArgs(env.Self), " "), err, tail(out)),
					Fix: "remove the server by hand: " + cmd + " " + strings.Join(removeArgs(env.Self), " ")}
			}
			return nil
		}
		return add, remove
	}
	claudeAdd, claudeRemove := cli("claude",
		func(self string) []string {
			return []string{"mcp", "add", "--scope", "user", ServerName, "--", self, "serve"}
		},
		func(string) []string { return []string{"mcp", "remove", "--scope", "user", ServerName} })
	codexAdd, codexRemove := cli("codex",
		func(self string) []string { return []string{"mcp", "add", ServerName, "--", self, "serve"} },
		func(string) []string { return []string{"mcp", "remove", ServerName} })
	geminiAdd, geminiRemove := cli("gemini",
		func(self string) []string {
			return []string{"mcp", "add", "--scope", "user", ServerName, self, "serve"}
		},
		func(string) []string { return []string{"mcp", "remove", "--scope", "user", ServerName} })
	return map[string]host{
		"claude": {name: "claude", present: onPath("claude"), add: claudeAdd, remove: claudeRemove},
		"codex":  {name: "codex", present: onPath("codex"), add: codexAdd, remove: codexRemove},
		"cursor": {name: "cursor", present: func(env Env) bool { _, err := os.Stat(filepath.Join(env.Home, ".cursor")); return err == nil },
			add: cursorAdd, remove: cursorRemove},
		"gemini": {name: "gemini", present: onPath("gemini"), add: geminiAdd, remove: geminiRemove},
	}
}

// AddServer registers the MCP server with each host. A host named explicitly is set up even when its command
// line is not found; with no hosts named, hosts that are not present are skipped and say so.
func AddServer(ctx context.Context, env Env, names []string) ([]HostResult, error) {
	return each(ctx, env, names, func(ctx context.Context, h host, res *HostResult) error {
		if err := h.add(ctx, env); err != nil {
			return err
		}
		res.Server = "added"
		return nil
	})
}

// RemoveServer unregisters the MCP server from each host.
func RemoveServer(ctx context.Context, env Env, names []string) ([]HostResult, error) {
	return each(ctx, env, names, func(ctx context.Context, h host, res *HostResult) error {
		if err := h.remove(ctx, env); err != nil {
			return err
		}
		res.Server = "removed"
		return nil
	})
}

func each(ctx context.Context, env Env, names []string, do func(context.Context, host, *HostResult) error) ([]HostResult, error) {
	if env.Home == "" || env.Self == "" || env.LookPath == nil || env.Run == nil {
		return nil, errors.New("install: incomplete environment")
	}
	all := hosts()
	explicit := len(names) > 0
	if !explicit {
		names = Hosts
	}
	var results []HostResult
	for _, n := range names {
		h, ok := all[n]
		if !ok {
			return nil, &headless.Refusal{Code: "invalid-args", Message: fmt.Sprintf("%q is not a host", n),
				Fix: "pass one or more of " + strings.Join(Hosts, ", ") + ", or none for every host on this machine"}
		}
		res := HostResult{Host: n}
		if !explicit && !h.present(env) {
			res.Server = "skipped: not found on this machine"
			results = append(results, res)
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, time.Minute)
		err := do(hctx, h, &res)
		cancel()
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// Cursor keeps its MCP servers in ~/.cursor/mcp.json.

func cursorFile(env Env) string { return filepath.Join(env.Home, ".cursor", "mcp.json") }

func cursorRead(env Env) (map[string]any, error) {
	cfg := map[string]any{}
	b, err := os.ReadFile(cursorFile(env))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", cursorFile(env), err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, &headless.Refusal{Code: "host-failed", Message: fmt.Sprintf("%s is not a JSON object: %v", cursorFile(env), err),
			Fix: "fix the file, or add the server by hand under mcpServers: {\"command\": \"" + env.Self + "\", \"args\": [\"serve\"]}"}
	}
	return cfg, nil
}

func cursorWrite(env Env, cfg map[string]any) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cursorFile(env)), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(cursorFile(env)), err)
	}
	if err := os.WriteFile(cursorFile(env), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", cursorFile(env), err)
	}
	return nil
}

func cursorAdd(_ context.Context, env Env) error {
	cfg, err := cursorRead(env)
	if err != nil {
		return err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[ServerName] = map[string]any{"command": env.Self, "args": []string{"serve"}}
	cfg["mcpServers"] = servers
	return cursorWrite(env, cfg)
}

func cursorRemove(_ context.Context, env Env) error {
	cfg, err := cursorRead(env)
	if err != nil {
		return err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if _, ok := servers[ServerName]; !ok {
		return nil
	}
	delete(servers, ServerName)
	return cursorWrite(env, cfg)
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return "…" + s[len(s)-500:]
	}
	return s
}
