package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tyrantkhan/artifact-helper/internal/fakeclaude"
	"github.com/tyrantkhan/artifact-helper/internal/headless"
	"github.com/tyrantkhan/artifact-helper/internal/skillinstall"
)

const exampleLogin = `{"loggedIn": true, "authMethod": "claude.ai", "email": "me@example.com", "orgName": "Example Inc"}`

// machine is one test machine: a fake claude, a config path, and how the streams look.
type machine struct {
	bin      string
	config   string
	environ  []string
	getenv   map[string]string
	terminal bool
	// skills keeps install and uninstall away from the real skills folder, should the gate ever let them run.
	skills *skillinstall.Env
	// stdin, when set, replaces the string a run is given.
	stdin io.Reader
}

func newMachine(t *testing.T, spec fakeclaude.Spec) *machine {
	t.Helper()
	if spec.Version == "" {
		spec.Version = "9.9.9 (Claude Code)"
	}
	if spec.Auth == "" {
		spec.Auth = exampleLogin
	}
	// The test may run in a session that sets CLAUDE_CONFIG_DIR; each test sets its own.
	var environ []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !strings.EqualFold(name, "CLAUDE_CONFIG_DIR") {
			environ = append(environ, kv)
		}
	}
	return &machine{bin: fakeclaude.Install(t, spec), config: filepath.Join(t.TempDir(), "artifact-helper", "config.json"),
		environ: environ, getenv: map[string]string{"NO_COLOR": "1"}, skills: fakeMachine(t)}
}

func (m *machine) run(stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	getenv := func(k string) string {
		if v, ok := m.getenv[k]; ok {
			return v
		}
		if k == "ARTIFACT_HELPER_CLAUDE" {
			return m.bin
		}
		return ""
	}
	var in io.Reader = strings.NewReader(stdin)
	if m.stdin != nil {
		in = m.stdin
	}
	e := &Env{Args: args, Stdin: in, Stdout: &out, Stderr: &errb, Getenv: getenv, Skills: m.skills,
		Environ: func() []string { return m.environ }, Config: m.config, Terminal: func(any) bool { return m.terminal }}
	return Main(e), out.String(), errb.String()
}

func (m *machine) saved(t *testing.T) *config {
	t.Helper()
	c, err := loadConfig(m.config)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func refusalCode(t *testing.T, out string) string {
	t.Helper()
	e, _ := result(t, out)["error"].(map[string]any)
	return e["code"].(string)
}

func TestEveryCommandButTheOpenOnesNeedsInit(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	for _, c := range commands() {
		code, out, _ := m.run("", c.name)
		if openCommands[c.name] {
			continue
		}
		if code != 1 || refusalCode(t, out) != "not-initialized" || !strings.Contains(out, "artifact-helper init") {
			t.Errorf("%s: code=%d out=%s", c.name, code, out)
		}
	}
	if code, out, _ := m.run("", "get-skill"); code != 0 || !strings.HasPrefix(out, "# artifact-helper guide") {
		t.Fatalf("get-skill: code=%d", code)
	}
	if code, out, _ := m.run("", "--version"); code != 0 || !strings.HasPrefix(out, "artifact-helper ") {
		t.Fatalf("--version: code=%d out=%q", code, out)
	}
	if code, _, errs := m.run("", "publish", "--help"); code != 0 || !strings.HasPrefix(errs, "Usage: artifact-helper publish") {
		t.Fatalf("publish --help: code=%d err=%q", code, errs)
	}
	if err := os.MkdirAll(filepath.Dir(m.config), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{", `{"claudeConfigDir": "relative"}`} {
		if err := os.WriteFile(m.config, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, out, _ := m.run("", "doctor"); code != 1 || refusalCode(t, out) != "invalid-config" {
			t.Errorf("%s: code=%d out=%s", bad, code, out)
		}
	}
}

func TestInitWithoutATerminal(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	other := t.TempDir()
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR="+other)

	// Bare, it reports what it found and saves nothing.
	code, out, _ := m.run("", "init")
	r := result(t, out)
	logins, _ := r["logins"].([]any)
	if code != 0 || r["saved"] != false || len(logins) != 2 || r["organization"] != "Example Inc" || r["claudeConfigDir"] != "default" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if m.saved(t) != nil {
		t.Fatal("init without --save wrote the config")
	}
	// --organization must agree with the account; a refusal saves nothing.
	if code, out, _ = m.run("", "init", "--save", "--organization", "Other Co"); code != 1 || refusalCode(t, out) != "wrong-organization" || m.saved(t) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if code, _, _ = m.run("", "init", "--save", "--claude-config-dir", "relative"); code != 1 || m.saved(t) != nil {
		t.Fatalf("a relative folder was taken: code=%d", code)
	}

	// --save keeps the default folder over the inherited one unless told otherwise.
	if code, out, _ = m.run("", "init", "--save"); code != 0 || result(t, out)["saved"] != true || result(t, out)["plugin"] != "skipped" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if c := m.saved(t); c.Organization != "Example Inc" || c.ClaudeConfigDir != "default" {
		t.Fatalf("saved %+v", c)
	}
	// Every command now takes the saved settings, with the environment still on top.
	code, out, _ = m.run("", "doctor")
	if r := result(t, out); code != 0 || r["expectedOrganization"] != "Example Inc" || r["configDir"] != "default" {
		t.Fatalf("doctor: code=%d out=%s", code, out)
	}
	m.getenv["ARTIFACT_HELPER_ORGANIZATION"] = "Other Co"
	if code, out, _ = m.run("", "doctor"); code != 1 || refusalCode(t, out) != "wrong-organization" {
		t.Fatalf("the environment did not override the config: code=%d out=%s", code, out)
	}
	delete(m.getenv, "ARTIFACT_HELPER_ORGANIZATION")

	if code, out, _ = m.run("", "init", "--save", "--claude-config-dir", other); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if c := m.saved(t); c.ClaudeConfigDir != other {
		t.Fatalf("saved %+v", c)
	}
}

func TestInitRefusesAFolderWithoutALogin(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{Auth: `{"loggedIn": false}`})
	if code, out, _ := m.run("", "init", "--save"); code != 1 || refusalCode(t, out) != "not-logged-in" || m.saved(t) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A login that names no organization saves none, so nothing is checked.
	m = newMachine(t, fakeclaude.Spec{Auth: `{"loggedIn": true, "authMethod": "oauth_token"}`})
	if code, out, _ := m.run("", "init", "--save"); code != 0 || m.saved(t).Organization != "" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestInitInstallsThePlugin(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	m := newMachine(t, fakeclaude.Spec{Record: record, Plugin: map[string]string{
		"list --json": "[]", "marketplace list --json": "[]",
		"marketplace add tyrantkhan/artifact-helper": "added", "install artifact-helper@artifact-helper": "installed",
	}})
	code, out, _ := m.run("", "init", "--save", "--plugin")
	if code != 0 || result(t, out)["plugin"] != "installed" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	var plugin []string
	for _, r := range fakeclaude.Runs(t, record) {
		if r[1] == "plugin" {
			plugin = append(plugin, strings.Join(r[2:], " "))
		}
	}
	if want := "list --json|marketplace list --json|marketplace add tyrantkhan/artifact-helper|install artifact-helper@artifact-helper"; strings.Join(plugin, "|") != want {
		t.Fatalf("plugin commands = %q", plugin)
	}
}

func TestInitInATerminal(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	m.terminal = true
	other := t.TempDir()
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR="+other)

	// Pick the second folder, take the default yes to saving, and decline the plugin.
	code, out, _ := m.run("2\n\nn\n", "init")
	for _, want := range []string{"artifact-helper init", "Claude accounts on this machine", "1  default", "2  " + other, "Use which?", "Save?", "Install the Claude Code plugin?", "+ saved"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "\x1b[") || json.Valid([]byte(out)) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if c := m.saved(t); c.ClaudeConfigDir != other || c.Organization != "Example Inc" {
		t.Fatalf("saved %+v", c)
	}

	// Input that ends before an answer saves nothing and says why, readably.
	m = newMachine(t, fakeclaude.Spec{})
	m.terminal = true
	code, out, _ = m.run("", "init")
	if code != 1 || !strings.Contains(out, "input ended") || !strings.Contains(out, "Fix:") || m.saved(t) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}

	// With a terminal that takes color, the view is painted.
	delete(m.getenv, "NO_COLOR")
	m.getenv["TERM"] = "xterm-256color"
	// Input that ends at the plugin question declines it, after the setup was saved.
	if code, out, _ = m.run("\n", "init"); code != 0 || !strings.Contains(out, "\x1b[1m") || m.saved(t) == nil {
		t.Fatalf("code=%d, color or save missing in:\n%q", code, out)
	}
}

func TestDoctorInATerminal(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	if err := saveConfig(m.config, config{Organization: "Example Inc", ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	m.terminal = true
	code, out, _ := m.run("", "doctor")
	for _, want := range []string{"doctor", "+ claude", "+ signed in     me@example.com in Example Inc", "+ organization  Example Inc", "config dir    default", m.config} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code != 0 || json.Valid([]byte(out)) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	m.getenv["ARTIFACT_HELPER_ORGANIZATION"] = "Other Co"
	code, out, _ = m.run("", "doctor")
	if code != 1 || !strings.Contains(out, "x organization  Other Co required") || !strings.Contains(out, "Fix:") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestInitProbesEachFolderWithItsOwnEnvironment(t *testing.T) {
	envRecord := filepath.Join(t.TempDir(), "env")
	m := newMachine(t, fakeclaude.Spec{EnvRecord: envRecord, EnvName: "CLAUDE_CONFIG_DIR"})
	other := t.TempDir()
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR="+other)
	if code, out, _ := m.run("", "init"); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	data, err := os.ReadFile(envRecord)
	if err != nil {
		t.Fatal(err)
	}
	// Each probe runs --version and auth status: the default folder without the variable, the other with it.
	want := []string{"CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIR=" + other, "CLAUDE_CONFIG_DIR=" + other}
	if got := strings.Split(strings.TrimSpace(string(data)), "\n"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("probes ran with %q", got)
	}
}

func TestInitReportsWhatItCannotSave(t *testing.T) {
	// Without --save, a folder with no login is reported, so the agent can still ask which to use.
	m := newMachine(t, fakeclaude.Spec{Auth: `{"loggedIn": false}`})
	code, out, _ := m.run("", "init")
	logins, _ := result(t, out)["logins"].([]any)
	if code != 0 || result(t, out)["saved"] != false || len(logins) != 1 || logins[0].(map[string]any)["loggedIn"] != false {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A relative CLAUDE_CONFIG_DIR is named but not offered, since no later run could use it.
	m = newMachine(t, fakeclaude.Spec{})
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR=relative/dir")
	code, out, _ = m.run("", "init")
	logins, _ = result(t, out)["logins"].([]any)
	if code != 0 || len(logins) != 1 || result(t, out)["unusableConfigDir"] != "relative/dir" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// An organization a login cannot report could never be checked, so init refuses it.
	m = newMachine(t, fakeclaude.Spec{Auth: `{"loggedIn": true, "authMethod": "oauth_token"}`})
	code, out, _ = m.run("", "init", "--save", "--organization", "Acme")
	if code != 1 || refusalCode(t, out) != "organization-unverifiable" || m.saved(t) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestInitKeepsTheResultWhenThePluginFails(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{Plugin: map[string]string{"list --json": "[]", "marketplace list --json": `[{"name": "artifact-helper"}]`}})
	code, out, _ := m.run("", "init", "--save", "--plugin")
	r := result(t, out)
	if code != 1 || r["ok"] != false || r["saved"] != true || r["plugin"] != "failed" || refusalCode(t, out) != "plugin-failed" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if m.saved(t) == nil {
		t.Fatal("the config was not saved")
	}
	// In a terminal, the saved row still shows above the failure.
	m.terminal = true
	code, out, _ = m.run("", "init", "--save", "--plugin")
	if code != 1 || !strings.Contains(out, "+ saved") || !strings.Contains(out, "x plugin") || !strings.Contains(out, "Fix:") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestInitAsksAgainAndTreatsNoPluginAnswerAsNo(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	m.terminal = true
	other := t.TempDir()
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR="+other)
	// Out-of-range and non-numeric picks ask again; input that ends at the plugin question declines it.
	code, out, _ := m.run("9\nx\n2\nmaybe\ny\n", "init")
	if code != 0 || strings.Count(out, "Use which?") != 3 || strings.Count(out, "Save?") != 2 || m.saved(t).ClaudeConfigDir != other {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestHelpWithoutInitOnlyPrintsUsage(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	m := newMachine(t, fakeclaude.Spec{Record: record})
	// --help here is a flag's value; without a config it still prints the usage instead of publishing.
	code, out, errs := m.run("", "publish", "page.html", "--title", "--help")
	if code != 0 || out != "" || !strings.HasPrefix(errs, "Usage: artifact-helper publish") {
		t.Fatalf("code=%d out=%q err=%.60q", code, out, errs)
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatal("claude ran without a config")
	}
	if code, _, errs = m.run("", "doctor", "--help=true"); code != 0 || !strings.HasPrefix(errs, "Usage: artifact-helper doctor") {
		t.Fatalf("code=%d err=%.60q", code, errs)
	}
}

func TestTheNullDeviceIsNoTerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(&Env{}, f) {
		t.Fatal("the null device passed for a terminal")
	}
}

func TestDoctorInATerminalWithoutClaude(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	if err := saveConfig(m.config, config{ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	m.terminal = true
	m.getenv["ARTIFACT_HELPER_CLAUDE"] = filepath.Join(t.TempDir(), "no-such-claude")
	code, out, _ := m.run("", "doctor")
	if code != 1 || !strings.Contains(out, "x claude") || !strings.Contains(out, "not usable") || !strings.Contains(out, "Fix:") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestInitInstallsThePluginWhateverTheFolderLogin(t *testing.T) {
	// The plugin goes into the calling Claude Code, so a chosen folder with no login does not stop it.
	m := newMachine(t, fakeclaude.Spec{Auth: `{"loggedIn": false}`, Plugin: map[string]string{
		"list --json": `[{"id": "artifact-helper@artifact-helper"}]`,
	}})
	code, out, _ := m.run("", "init", "--plugin")
	if code != 0 || result(t, out)["plugin"] != "already-installed" || result(t, out)["saved"] != false {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

// brokenInput gives its lines, then fails as a terminal that went away would.
type brokenInput struct{ lines string }

func (b *brokenInput) Read(p []byte) (int, error) {
	if b.lines == "" {
		return 0, errors.New("terminal gone")
	}
	n := copy(p, b.lines)
	b.lines = b.lines[n:]
	return n, nil
}

func TestConfigFileTrouble(t *testing.T) {
	if p, err := configPath(&Env{}); err != nil || filepath.Base(p) != "config.json" || filepath.Base(filepath.Dir(p)) != "artifact-helper" {
		t.Fatalf("default path %q, %v", p, err)
	}
	dir := t.TempDir()
	// A folder where the file belongs can be neither read nor written.
	if _, err := loadConfig(dir); err == nil || refusalOf(err) != "invalid-config" {
		t.Fatalf("loading a folder: %v", err)
	}
	if err := saveConfig(dir, config{ClaudeConfigDir: "default"}); err == nil {
		t.Fatal("saved over a folder")
	}
	// A file where a folder belongs cannot hold one.
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(filepath.Join(file, "artifact-helper", "config.json"), config{ClaudeConfigDir: "default"}); err == nil {
		t.Fatal("saved under a file")
	}
	// options takes the environment alone without a config, and refuses a damaged one.
	e := &Env{Getenv: func(string) string { return "" }, Environ: func() []string { return nil }, Config: filepath.Join(dir, "none.json")}
	if o, err := options(e); err != nil || o.Organization != "" || o.ConfigDir != "" {
		t.Fatalf("options = %+v, %v", o, err)
	}
	e.Config = dir
	if _, err := options(e); refusalOf(err) != "invalid-config" {
		t.Fatalf("options on a damaged config: %v", err)
	}
	// Without a home there is no user config folder, and every command says so.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("AppData", "")
	e.Config = ""
	if _, err := options(e); err == nil {
		t.Fatal("found a config folder without a home")
	}
	for _, args := range [][]string{{"doctor"}, {"init"}} {
		var out, errb bytes.Buffer
		e := &Env{Args: args, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
			Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
		if code := Main(e); code != 1 || refusalCode(t, out.String()) != "error" || !strings.Contains(errb.String(), "user config folder") {
			t.Errorf("%v: code=%d out=%s err=%s", args, code, out.String(), errb.String())
		}
	}
}

func refusalOf(err error) string {
	if rf, ok := headless.IsRefusal(err); ok {
		return rf.Code
	}
	return ""
}

func TestLoginAccount(t *testing.T) {
	for l, want := range map[login]string{
		{}: "not signed in",
		{LoggedIn: true, Email: "a@x", Organization: "Org"}: "a@x in Org",
		{LoggedIn: true, Email: "a@x"}:                      "a@x, no organization",
		{LoggedIn: true, Organization: "Org"}:               "Org",
		{LoggedIn: true, AuthMethod: "oauth_token"}:         "an account claude does not name (oauth_token)",
	} {
		if got := l.account(); got != want {
			t.Errorf("%+v: %q, want %q", l, got, want)
		}
	}
}

func TestInitRefusals(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{})
	if code, out, _ := m.run("", "init", "extra"); code != 1 || refusalCode(t, out) != "invalid-args" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	m.getenv["ARTIFACT_HELPER_TIMEOUT"] = "soon"
	if code, out, _ := m.run("", "init"); code != 1 || refusalCode(t, out) != "invalid-env" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	delete(m.getenv, "ARTIFACT_HELPER_TIMEOUT")
	if code, out, _ := m.run("", "publish", "--", "--help"); code != 1 || refusalCode(t, out) != "not-initialized" {
		t.Fatalf("-- did not end the flags: code=%d out=%s", code, out)
	}
	// A claude that cannot run stops init before there are any logins to show.
	m.getenv["ARTIFACT_HELPER_CLAUDE"] = filepath.Join(t.TempDir(), "no-such-claude")
	if code, out, _ := m.run("", "init"); code != 1 || refusalCode(t, out) != "no-claude" || strings.Contains(out, "logins") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A config that cannot be written is an error after the logins, which the result still carries.
	m = newMachine(t, fakeclaude.Spec{})
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.config = filepath.Join(file, "config.json")
	code, out, errs := m.run("", "init", "--save")
	if r := result(t, out); code != 1 || refusalCode(t, out) != "error" || r["saved"] != false || r["logins"] == nil || errs == "" {
		t.Fatalf("code=%d out=%s err=%s", code, out, errs)
	}
	m.terminal = true
	if code, out, _ = m.run("", "init", "--save"); code != 1 || !strings.Contains(out, "x not saved") || !strings.Contains(out, "creating") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestInitInATerminalShowsEveryCase(t *testing.T) {
	inTerminal := func(spec fakeclaude.Spec) *machine {
		m := newMachine(t, spec)
		m.terminal = true
		return m
	}
	// The plugin rows, installed and already there.
	plugins := map[string]string{"list --json": "[]", "marketplace list --json": `[{"name": "artifact-helper"}]`, "install artifact-helper@artifact-helper": ""}
	if code, out, _ := inTerminal(fakeclaude.Spec{Plugin: plugins}).run("\ny\n", "init"); code != 0 || !strings.Contains(out, "+ plugin        artifact-helper@artifact-helper installed") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	already := map[string]string{"list --json": `[{"id": "artifact-helper@artifact-helper"}]`}
	if code, out, _ := inTerminal(fakeclaude.Spec{Plugin: already}).run("", "init", "--save", "--plugin"); code != 0 || !strings.Contains(out, "was already installed") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A login without an organization, and an inherited folder init cannot use.
	m := inTerminal(fakeclaude.Spec{Auth: `{"loggedIn": true, "authMethod": "oauth_token"}`})
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR=relative/dir")
	if code, out, _ := m.run("n\n", "init"); code != 0 || !strings.Contains(out, "is not an absolute path") || !strings.Contains(out, "names no organization") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// Two folders: an empty pick takes the first, and input that ends at the pick saves nothing.
	m = inTerminal(fakeclaude.Spec{})
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR="+t.TempDir())
	if code, out, _ := m.run("\n\nn\n", "init"); code != 0 || m.saved(t).ClaudeConfigDir != "default" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	m = inTerminal(fakeclaude.Spec{})
	m.environ = append(m.environ, "CLAUDE_CONFIG_DIR="+t.TempDir())
	if code, out, _ := m.run("", "init"); code != 1 || !strings.Contains(out, "input ended") || m.saved(t) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A terminal that fails mid-answer stops init, before or after the save.
	m = inTerminal(fakeclaude.Spec{})
	m.stdin = &brokenInput{}
	if code, out, _ := m.run("", "init"); code != 1 || !strings.Contains(out, "terminal gone") || m.saved(t) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	m = inTerminal(fakeclaude.Spec{})
	m.stdin = &brokenInput{lines: "\n"}
	if code, out, _ := m.run("", "init"); code != 1 || !strings.Contains(out, "terminal gone") || m.saved(t) == nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// Declined, in color.
	m = inTerminal(fakeclaude.Spec{})
	delete(m.getenv, "NO_COLOR")
	m.getenv["TERM"] = "xterm"
	if code, out, _ := m.run("n\n", "init"); code != 0 || !strings.Contains(out, "✗") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDoctorInATerminalShowsEveryRow(t *testing.T) {
	m := newMachine(t, fakeclaude.Spec{Auth: `{"loggedIn": false}`})
	if err := saveConfig(m.config, config{Organization: "Example Inc", ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	m.terminal = true
	code, out, _ := m.run("", "doctor")
	if code != 1 || !strings.Contains(out, "x signed in     no") || !strings.Contains(out, "Example Inc required (not checked)") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	m = newMachine(t, fakeclaude.Spec{})
	if err := saveConfig(m.config, config{ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	m.terminal = true
	if code, out, _ = m.run("", "doctor"); code != 0 || !strings.Contains(out, "organization  not checked") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestARegularFileIsNoTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(&Env{}, f) || isTerminal(&Env{}, strings.NewReader("")) {
		t.Fatal("a file or a reader passed for a terminal")
	}
}

func TestOptionFlagsReadLikeTheirVariables(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	m := newMachine(t, fakeclaude.Spec{Record: record, Outputs: map[string]string{"": linkOK}})
	if err := saveConfig(m.config, config{Organization: "Example Inc", ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "p.html")
	if err := os.WriteFile(p, []byte("<title>p</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	lastRun := func() []string {
		t.Helper()
		var last []string
		for _, r := range fakeclaude.Runs(t, record) {
			if r[1] == "-p" {
				last = r
			}
		}
		return last
	}
	// An empty --organization would switch the check off, so it is refused; a padded name is trimmed.
	for _, v := range []string{"", "   "} {
		if code, out, _ := m.run("", "publish", p, "--organization", v); code != 1 || refusalCode(t, out) != "invalid-args" {
			t.Errorf("--organization %q: code=%d out=%s", v, code, out)
		}
	}
	if code, out, _ := m.run("", "publish", p, "--organization", "  example inc "); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// 0 means no cap from the flag, as it does from the variable.
	if code, out, _ := m.run("", "publish", p, "--max-budget-usd", "0"); code != 0 || slices.Contains(lastRun(), "--max-budget-usd") {
		t.Fatalf("code=%d out=%s args=%v", code, out, lastRun())
	}
	for _, args := range [][]string{{"--max-budget-usd", "-1"}, {"--max-budget-usd", "NaN"}, {"--timeout", "0"}} {
		if code, out, _ := m.run("", append([]string{"publish", p}, args...)...); code != 1 || refusalCode(t, out) != "invalid-args" {
			t.Errorf("%v: code=%d out=%s", args, code, out)
		}
	}
	// --verbose=false stays quiet, and a typo in the variable is refused rather than read as on.
	if _, _, errs := m.run("", "publish", p, "--verbose=false"); errs != "" {
		t.Fatalf("logged: %s", errs)
	}
	if _, _, errs := m.run("", "publish", p, "--verbose"); !strings.Contains(errs, "running") {
		t.Fatalf("no log: %q", errs)
	}
	m.getenv["ARTIFACT_HELPER_VERBOSE"] = "maybe"
	if code, out, _ := m.run("", "publish", p); code != 1 || refusalCode(t, out) != "invalid-env" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestTheSavedOrganizationIDDecides(t *testing.T) {
	other := `{"loggedIn": true, "authMethod": "claude.ai", "email": "me@example.com", "orgName": "Example Inc", "orgId": "org-2"}`
	m := newMachine(t, fakeclaude.Spec{Auth: other})
	if err := saveConfig(m.config, config{Organization: "Example Inc", OrganizationID: "org-1", ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	// Same name, another organization: refused, and the refusal keeps what doctor found.
	code, out, _ := m.run("", "doctor")
	if r := result(t, out); code != 1 || refusalCode(t, out) != "wrong-organization" || r["loggedIn"] != true || r["organizationId"] != "org-2" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// An organization named in the environment or by flag is checked by name.
	m.getenv["ARTIFACT_HELPER_ORGANIZATION"] = "Example Inc"
	if code, out, _ = m.run("", "doctor"); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	delete(m.getenv, "ARTIFACT_HELPER_ORGANIZATION")
	if code, out, _ = m.run("", "doctor", "--organization", "Example Inc"); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// init saves the ID of the account it checked.
	m = newMachine(t, fakeclaude.Spec{Auth: other})
	if code, out, _ = m.run("", "init", "--save"); code != 0 || m.saved(t).OrganizationID != "org-2" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestHelpStatesTheLimitsTheCodeUses(t *testing.T) {
	for _, c := range []struct{ name, help, want string }{
		{"list", listHelp, fmt.Sprintf("default %d, at most %d", headless.DefaultListLimit, headless.MaxListLimit)},
		{"read", readHelp, "default " + headless.DefaultPage},
		{"publish", publishHelp, "(default " + headless.DefaultIcon + ")"},
		{"init", initHelp, "claude plugin marketplace add " + headless.PluginMarketplace},
		{"init", initHelp, "claude plugin install " + headless.PluginID},
	} {
		if !strings.Contains(c.help, c.want) {
			t.Errorf("help %s does not say %q", c.name, c.want)
		}
	}
}
