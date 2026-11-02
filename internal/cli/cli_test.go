package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyrantkhan/artifact-helper/internal/fakeclaude"
	"github.com/tyrantkhan/artifact-helper/internal/skillinstall"
)

func TestMain(m *testing.M) {
	fakeclaude.Main()
	os.Exit(m.Run())
}

const (
	link       = "https://claude.ai/artifact/Abc123"
	threadUUID = "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b"
	linkOK     = `{"type":"result","subtype":"success","num_turns":3,"total_cost_usd":0.1,"duration_ms":10,"structured_output":{"url":"` + link + `","error":""}}`
)

func fakeClaude(t *testing.T, outputs map[string]string, files map[string]string) string {
	t.Helper()
	return fakeclaude.Install(t, fakeclaude.Spec{Version: "9.9.9", Auth: `{"loggedIn":true,"authMethod":"claude.ai"}`, Outputs: outputs, Files: files})
}

func run(t *testing.T, env map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	e := &Env{Args: args, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		Getenv: testGetenv(t, env), Environ: func() []string { return nil }, Config: savedConfig(t, config{ClaudeConfigDir: "default"})}
	return Main(e), out.String(), errb.String()
}

// testGetenv reads env, and gives a test that names no claude one that does not exist, so no test ever
// reaches the real claude on PATH, whatever order a command checks things in.
func testGetenv(t *testing.T, env map[string]string) func(string) string {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "no-claude-in-tests")
	return func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		if k == "ARTIFACT_HELPER_CLAUDE" {
			return missing
		}
		return ""
	}
}

// savedConfig writes c where init would, in a temporary folder, so no test reads or writes the real one.
func savedConfig(t *testing.T, c config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact-helper", "config.json")
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	return path
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
	if code != 0 || !strings.Contains(out, "Usage: artifact-helper <command>") || !strings.Contains(out, "ARTIFACT_HELPER_MODEL") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	for _, c := range commands() {
		code, out, _ = run(t, nil, "", "help", c.name)
		if code != 0 || !strings.HasPrefix(out, "Usage: artifact-helper "+c.name) {
			t.Fatalf("help %s: code=%d out=%q", c.name, code, out)
		}
	}
	code, out, _ = run(t, nil, "", "--version")
	if code != 0 || out != "artifact-helper "+Version+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, _, errs := run(t, nil, "", "bogus")
	if code != 2 || !strings.Contains(errs, `unknown command "bogus"`) {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestPublishPrintsJSON(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"publish": linkOK}, nil)
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ARTIFACT_HELPER_CLAUDE": bin, "ARTIFACT_HELPER_MODEL": "haiku"}
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
	// The fake claude writes its files into the folder each run starts in, and runs with no folder of their
	// own start in this one, so work from a temporary folder rather than the source tree.
	t.Chdir(t.TempDir())
	readOK := `{"type":"result","subtype":"success","num_turns":2,"structured_output":{"path":"index.html","error":""}}`
	listing := fakeclaude.ToolRun("Artifact", `{}`, "1 thread", false, `{"type":"result","subtype":"success","structured_output":{"threads":[{"id":"`+threadUUID+`","author":"Sam","text":"Typo","resolved":false,"activated":true,"replies":[]}],"more_cursor":"","error":""}}`)
	threadOK := `{"type":"result","subtype":"success","structured_output":{"replied":true,"resolved":true,"message":"posted","error":""}}`
	done := `{"type":"result","subtype":"success","result":"Listed."}`
	files := fakeclaude.ToolRun("Artifact", `{"file_list":{"ver":"v1","files":[{"path":"assets/app.js"}]}}`, "Published files", false, done)
	artifacts := fakeclaude.ToolRun("Artifact", `{"artifacts":[{"title":"Q3","url":"`+link+`","updatedAt":"2026-11-01T09:30:00Z","rel":"mine"}],"total":21}`, "Showing 1 of 21", false, done)
	bin := fakeClaude(t, map[string]string{"read": readOK, "publish": linkOK, "comments": listing, "reply": threadOK, "resolve": threadOK, "files": files, "list": artifacts},
		map[string]string{"index.html": "<h1>Hello</h1>"})
	env := map[string]string{"ARTIFACT_HELPER_CLAUDE": bin}
	out := t.TempDir()

	code, o, _ := run(t, env, "", "read", link, "--out", out)
	res := result(t, o)
	saved := filepath.Join(out, "index.html")
	if code != 0 || res["command"] != "read" || res["savedPath"] != saved || res["path"] != "index.html" {
		t.Fatalf("exit %d: %s", code, o)
	}

	code, o, _ = run(t, env, "", "read", link, "--list")
	res = result(t, o)
	if code != 0 || res["command"] != "read" || len(res["files"].([]any)) != 1 || res["version"] != "v1" {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, "", "read", link, "--list", "--out", out)
	if code != 1 || result(t, o)["error"].(map[string]any)["code"] != "invalid-args" {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, "", "list", "--scope", "all", "--limit", "5")
	res = result(t, o)
	if code != 0 || res["scope"] != "all" || len(res["artifacts"].([]any)) != 1 || res["total"] != 21.0 {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, "", "list", "extra")
	if code != 1 || result(t, o)["error"].(map[string]any)["code"] != "invalid-args" {
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

func TestShare(t *testing.T) {
	ok := `{"type":"result","subtype":"success","structured_output":{"shared":true,"message":"Shared","error":""}}`
	bin := fakeClaude(t, map[string]string{"share": ok}, nil)
	env := map[string]string{"ARTIFACT_HELPER_CLAUDE": bin}
	code, o, _ := run(t, env, "", "share", link, "--org")
	if code != 1 || result(t, o)["error"].(map[string]any)["code"] != "share-disabled" {
		t.Fatalf("exit %d: %s", code, o)
	}
	code, o, _ = run(t, env, "", "share", link, "--org", "--allow-share")
	res := result(t, o)
	if code != 0 || res["mode"] != "org" || res["message"] != "Shared" {
		t.Fatalf("exit %d: %s", code, o)
	}
	env["ARTIFACT_HELPER_ALLOW_SHARE"] = "1"
	code, o, _ = run(t, env, "", "share", link, "--people", "sam@example.com, Ada", "--access", "view")
	res = result(t, o)
	if code != 0 || res["mode"] != "people" || len(res["people"].([]any)) != 2 || res["access"] != "view" {
		t.Fatalf("exit %d: %s", code, o)
	}
	for _, args := range [][]string{{"share", link}, {"share", link, "--org", "--people", "Ada"}, {"share", link, "--people", "Ada", "--access", "edit"}} {
		code, o, _ = run(t, env, "", args...)
		if code != 1 || result(t, o)["error"].(map[string]any)["code"] != "invalid-args" {
			t.Errorf("%v: exit %d: %s", args, code, o)
		}
	}
}

func TestRefusalsAreJSONOnStdout(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"": ""}, nil)
	withBin := map[string]string{"ARTIFACT_HELPER_CLAUDE": bin}
	cases := []struct {
		args []string
		env  map[string]string
		code string
	}{
		{[]string{"publish"}, nil, "invalid-args"},
		{[]string{"publish", "a.html", "b.html"}, nil, "invalid-args"},
		{[]string{"publish", "/no/such.html"}, withBin, "invalid-file"},
		{[]string{"publish", "/no/such.html"}, map[string]string{"ARTIFACT_HELPER_TIMEOUT": "never"}, "invalid-env"},
		{[]string{"read", "https://example.com/x"}, withBin, "invalid-url"},
		{[]string{"edit", link, "--from", "/no/such.json"}, withBin, "invalid-input"},
		{[]string{"comments"}, withBin, "invalid-args"},
		{[]string{"reply", link}, withBin, "invalid-args"},
		{[]string{"reply", link, threadUUID}, withBin, "invalid-args"},
		{[]string{"resolve", link, "nope"}, withBin, "invalid-thread"},
		{[]string{"doctor"}, map[string]string{"ARTIFACT_HELPER_CLAUDE": "/no/such/claude"}, "no-claude"},
		{[]string{"doctor", "extra"}, nil, "invalid-args"},
	}
	for _, c := range cases {
		code, out, _ := run(t, c.env, "", c.args...)
		res := result(t, out)
		e, _ := res["error"].(map[string]any)
		// fix is omitted when empty, so a missing one decodes as nil: compare the string, not the field.
		if fix, _ := e["fix"].(string); code != 1 || res["ok"] != false || e["code"] != c.code || fix == "" {
			t.Errorf("%v: code=%d result=%+v", c.args, code, res)
		}
	}
}

func TestDoctorPrintsJSON(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"": ""}, nil)
	code, out, _ := run(t, map[string]string{"ARTIFACT_HELPER_CLAUDE": bin}, "", "doctor")
	if code != 0 || !strings.Contains(out, `"loggedIn": true`) || !strings.Contains(out, `"version": "9.9.9"`) {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func fakeMachine(t *testing.T) *skillinstall.Env {
	t.Helper()
	home := t.TempDir()
	return &skillinstall.Env{Home: home, Cwd: filepath.Join(home, "project"), Getenv: func(string) string { return "" }, Now: time.Now}
}

func runMachine(t *testing.T, senv *skillinstall.Env, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	e := &Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard,
		Getenv: testGetenv(t, nil), Environ: func() []string { return nil }, Skills: senv,
		Config: savedConfig(t, config{ClaudeConfigDir: "default"})}
	return Main(e), out.String()
}

func TestGetSkill(t *testing.T) {
	code, out, _ := run(t, nil, "", "get-skill")
	if code != 0 || !strings.HasPrefix(out, "# artifact-helper guide") || !strings.Contains(out, "artifact-helper publish") {
		t.Fatalf("exit %d: %.80s", code, out)
	}
	code, out, _ = run(t, nil, "", "get-skill", "artifact-helper", "--json")
	if code != 0 || result(t, out)["version"] != Version || !strings.Contains(out, `"guide"`) {
		t.Fatalf("exit %d: %.80s", code, out)
	}
	code, out, _ = run(t, nil, "", "get-skill", "nope")
	if code != 1 || result(t, out)["error"].(map[string]any)["code"] != "invalid-args" {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestInstallAndUninstall(t *testing.T) {
	senv := fakeMachine(t)
	dir := filepath.Join(senv.Home, ".claude", "skills", "artifact-helper")
	code, out := runMachine(t, senv, "install", "--dry-run")
	res := result(t, out)
	if code != 0 || res["dryRun"] != true || !strings.Contains(res["summary"].(string), "1 written") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a dry run wrote")
	}
	code, out = runMachine(t, senv, "install")
	res = result(t, out)
	if code != 0 || res["command"] != "install" || !strings.Contains(res["summary"].(string), "1 written") || len(res["outcomes"].([]any)) != 1 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	code, out = runMachine(t, senv, "install")
	if code != 0 || !strings.Contains(result(t, out)["summary"].(string), "1 unchanged") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: artifact-helper\n---\nmine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runMachine(t, senv, "install")
	if code != 0 || !strings.Contains(result(t, out)["summary"].(string), "left alone: You've changed this copy") {
		t.Fatalf("exit %d: %s", code, out)
	}
	code, out = runMachine(t, senv, "install", "--force")
	if code != 0 || !strings.Contains(result(t, out)["summary"].(string), "1 replaced") {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, args := range [][]string{{"install", "extra"}, {"uninstall", "extra"}} {
		code, out = runMachine(t, senv, args...)
		if code != 1 || result(t, out)["error"].(map[string]any)["code"] != "invalid-args" {
			t.Errorf("%v: exit %d: %s", args, code, out)
		}
	}
	code, out = runMachine(t, senv, "uninstall")
	res = result(t, out)
	if code != 0 || len(res["skills"].([]any)) != 1 || res["skills"].([]any)[0].(map[string]any)["state"] != "removed" {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the skill is still there")
	}
	code, out = runMachine(t, senv, "uninstall")
	if code != 0 || len(result(t, out)["skills"].([]any)) != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestVersionMatchesEveryManifest(t *testing.T) {
	for _, p := range []string{".claude-plugin/plugin.json"} {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		var m struct{ Version string }
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if m.Version != Version {
			t.Errorf("%s has version %q, the CLI is %q", p, m.Version, Version)
		}
	}
}

func TestEveryCommandRefusesAnUnknownFlag(t *testing.T) {
	for _, c := range commands() {
		code, out, errs := run(t, nil, "", c.name, "--bogus")
		e, _ := result(t, out)["error"].(map[string]any)
		if code != 1 || e["code"] != "invalid-args" || e["fix"] != "run artifact-helper help "+c.name || !strings.Contains(errs, "Usage: artifact-helper "+c.name) {
			t.Errorf("%s: code=%d out=%q err=%q", c.name, code, out, errs)
		}
	}
}

func TestVerboseLogsAndDoubleDashEndsFlags(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"publish": linkOK}, nil)
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(t, map[string]string{"ARTIFACT_HELPER_CLAUDE": bin}, "", "publish", "--verbose", "--title", "x", "--", p)
	if code != 0 || !strings.Contains(errs, "running ") {
		t.Fatalf("exit %d: %s\n%s", code, out, errs)
	}
	code, out, errs = run(t, map[string]string{"ARTIFACT_HELPER_CLAUDE": bin, "ARTIFACT_HELPER_VERBOSE": "1"}, "", "publish", p)
	if code != 0 || !strings.Contains(errs, "running ") {
		t.Fatalf("exit %d: %s\n%s", code, out, errs)
	}
}

func TestGetSkillTakesOneNameAtMost(t *testing.T) {
	code, out, _ := run(t, nil, "", "get-skill", "artifact-helper", "another")
	if e, _ := result(t, out)["error"].(map[string]any); code != 1 || e["code"] != "invalid-args" {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestInstallUsesTheHomeDirectory(t *testing.T) {
	// No injected machine: install looks up the real home directory.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	code, out, errs := run(t, nil, "", "install", "--dry-run")
	var r struct{ Plan skillinstall.Plan }
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 || len(r.Plan.Skills) != 1 || len(r.Plan.Skills[0].Ops) != 1 {
		t.Fatalf("exit %d: %s\n%s", code, out, errs)
	}
	if p := r.Plan.Skills[0].Ops[0].Path; !strings.HasPrefix(p, home) {
		t.Fatalf("planned %s, outside %s", p, home)
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	code, _, errs = run(t, nil, "", "install", "--dry-run")
	if code != 1 || !strings.Contains(errs, "finding the home directory") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}

func TestSummarizeCountsWhatHappened(t *testing.T) {
	plan := &skillinstall.Plan{Skills: []skillinstall.Entry{
		{Name: "a", Ops: []skillinstall.Op{{Kind: skillinstall.Update}, {Kind: skillinstall.Write}, {Kind: skillinstall.Write}}},
		{Name: "b"},
	}}
	outs := []skillinstall.Outcome{{Ops: []skillinstall.OpOutcome{{Status: "done"}, {Status: "failed"}, {Status: "changed"}}}, {}}
	if got, want := summarize(plan, outs), "a: 1 updated · 1 changed meanwhile · 1 failed\nb: nothing to do"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got := (&exitError{code: 3}).Error(); got != "exit 3" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestHelpAndLocalInstallWithASavedConfig(t *testing.T) {
	// With a config saved, --help reaches the flag parser, which prints the usage.
	if code, _, errs := run(t, nil, "", "publish", "--help"); code != 0 || !strings.HasPrefix(errs, "Usage: artifact-helper publish") {
		t.Fatalf("code=%d err=%.60q", code, errs)
	}
	// With no injected machine, a local install looks up the current folder, and needs no home.
	t.Chdir(t.TempDir())
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	code, out, errs := run(t, nil, "", "install", "--local", "--dry-run")
	if r := result(t, out); code != 0 || r["plan"].(map[string]any)["scope"] != "local" {
		t.Fatalf("code=%d out=%s err=%s", code, out, errs)
	}
}
