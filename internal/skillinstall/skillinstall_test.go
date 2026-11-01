package skillinstall

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
	"github.com/tyrantkhan/claude-artifact-helper/skills"
)

const name = "artifact-helper"

func env(t *testing.T, present ...string) Env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("links are not made on Windows")
	}
	home := t.TempDir()
	for _, p := range present {
		if err := os.MkdirAll(filepath.Join(home, "."+p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return Env{Home: home, Cwd: filepath.Join(home, "project"), Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Now:      func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }}
}

func stub() []byte { s, _ := skills.Find(name); return s.Stub }

func opsByPath(e Entry) map[string]Op {
	m := map[string]Op{}
	for _, op := range e.Ops {
		m[op.Path] = op
	}
	return m
}

func TestInstallWritesSharedCopyAndLinksAgents(t *testing.T) {
	e := env(t, "claude", "cursor", "codex")
	p, err := Make(e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Scope != "global" || len(p.Skills) != 1 || p.Digest == "" {
		t.Fatalf("plan = %+v", p)
	}
	shared := filepath.Join(e.Home, ".agents", "skills", name)
	ops := opsByPath(p.Skills[0])
	if op := ops[shared]; op.Kind != Write || op.Role != "shared" || strings.Join(op.Agents, ",") != "codex" {
		t.Fatalf("shared op = %+v", op)
	}
	for _, a := range []string{".claude/skills", ".cursor/skills"} {
		op := ops[filepath.Join(e.Home, a, name)]
		if op.Kind != Link || op.LinkTarget != shared || op.Content != "link" {
			t.Fatalf("%s op = %+v", a, op)
		}
	}
	if _, ok := ops[filepath.Join(e.Home, ".gemini", "skills", name)]; ok {
		t.Fatal("gemini is not present but was planned")
	}
	outs := Execute(e, p)
	if Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	b, err := os.ReadFile(filepath.Join(shared, "SKILL.md"))
	if err != nil || string(b) != string(stub()) {
		t.Fatalf("shared copy: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(e.Home, ".claude", "skills", name, "SKILL.md")); err != nil || string(b) != string(stub()) {
		t.Fatalf("through the link: %v", err)
	}
	// A second plan keeps everything.
	p, _ = Make(e, Options{})
	for _, op := range p.Skills[0].Ops {
		if op.Kind != Keep {
			t.Errorf("second plan: %s %s", op.Kind, op.Path)
		}
	}
	found, _ := Scan(e, false)
	var states []string
	for _, f := range found {
		states = append(states, f.State)
	}
	if strings.Join(states, ",") != "current,link,link" {
		t.Fatalf("scan = %+v", found)
	}
}

func TestUpdateOnlyTouchesExistingCopies(t *testing.T) {
	e := env(t, "claude", "cursor")
	// An older released stub sits in Claude's folder; nothing else is installed.
	old := filepath.Join(e.Home, ".claude", "skills", name)
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	older := []byte(strings.Replace(string(stub()), "# artifact-helper", "# artifact-helper (old)", 1))
	s, _ := skills.Find(name)
	s.Released = append(s.Released, skills.Digest(older))
	withOld := func(fn func()) {
		orig := releasedExtra
		releasedExtra = s.Released
		defer func() { releasedExtra = orig }()
		fn()
	}
	if err := os.WriteFile(filepath.Join(old, "SKILL.md"), older, 0o644); err != nil {
		t.Fatal(err)
	}
	withOld(func() {
		p, err := Make(e, Options{Update: true})
		if err != nil {
			t.Fatal(err)
		}
		ops := opsByPath(p.Skills[0])
		if len(ops) != 1 {
			t.Fatalf("update planned %d ops, want only Claude's: %+v", len(ops), ops)
		}
		op := ops[old]
		// With no shared copy to link to, the outdated copy is updated in place.
		if op.Kind != Update || op.Content != "files" {
			t.Fatalf("op = %+v", op)
		}
		outs := Execute(e, p)
		if Failed(outs) {
			t.Fatalf("outs = %+v", outs)
		}
		if b, _ := os.ReadFile(filepath.Join(old, "SKILL.md")); string(b) != string(stub()) {
			t.Fatal("the old copy was not updated")
		}
	})
}

func TestEditedAndForeignCopiesAreLeftAloneUnlessForced(t *testing.T) {
	e := env(t, "claude")
	shared := filepath.Join(e.Home, ".agents", "skills", name)
	edited := []byte(string(stub()) + "\nMy own note.\n")
	write(t, filepath.Join(shared, "SKILL.md"), edited)
	foreign := filepath.Join(e.Home, ".claude", "skills", name)
	write(t, filepath.Join(foreign, "SKILL.md"), []byte("---\nname: something-else\n---\n# other\n"))
	write(t, filepath.Join(foreign, "script.sh"), []byte("#!/bin/sh\n"))

	p, err := Make(e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops := opsByPath(p.Skills[0])
	if op := ops[shared]; op.Kind != Leave || op.Reason != ReasonEdited || !op.Replaceable {
		t.Fatalf("shared op = %+v", op)
	}
	if op := ops[foreign]; op.Kind != Leave || op.Reason != ReasonOther || !op.Replaceable {
		t.Fatalf("foreign op = %+v", op)
	}
	outs := Execute(e, p)
	if Failed(outs) || len(outs[0].Ops) != 2 || outs[0].Ops[0].Status != "done" {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "SKILL.md")); string(b) != string(edited) {
		t.Fatal("the edited copy was touched")
	}

	p, _ = Make(e, Options{Force: true})
	ops = opsByPath(p.Skills[0])
	if ops[shared].Kind != Replace || ops[shared].Content != "files" || ops[foreign].Kind != Replace || ops[foreign].Content != "link" {
		t.Fatalf("forced ops = %+v", ops)
	}
	outs = Execute(e, p)
	if Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	backups := filepath.Join(e.Home, ".agents", ".artifact-helper-skill-backups")
	entries, _ := os.ReadDir(backups)
	if len(entries) != 2 {
		t.Fatalf("backups = %v", entries)
	}
	for _, op := range outs[0].Ops {
		if op.BackupPath == "" || !strings.HasPrefix(op.BackupPath, backups) {
			t.Errorf("op %s has backup %q", op.Path, op.BackupPath)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "SKILL.md")); string(b) != string(stub()) {
		t.Fatal("the shared copy was not replaced")
	}
	if o := observe(foreign); o.Kind != "link" || o.Target != shared {
		t.Fatalf("foreign now = %+v", o)
	}
	if b, _ := os.ReadFile(filepath.Join(op(outs, foreign).BackupPath, "script.sh")); string(b) != "#!/bin/sh\n" {
		t.Fatal("the backup lacks the foreign files")
	}
}

func op(outs []Outcome, path string) OpOutcome {
	for _, o := range outs[0].Ops {
		if o.Path == path {
			return o
		}
	}
	return OpOutcome{}
}

func TestExecuteRefusesAChangedPath(t *testing.T) {
	e := env(t, "claude")
	p, _ := Make(e, Options{})
	shared := filepath.Join(e.Home, ".agents", "skills", name)
	write(t, filepath.Join(shared, "SKILL.md"), []byte("changed meanwhile"))
	outs := Execute(e, p)
	if !Failed(outs) || outs[0].Status != "changed" || op(outs, shared).Status != "changed" {
		t.Fatalf("outs = %+v", outs)
	}
	// The link that depends on the shared copy is skipped, not made.
	if o := op(outs, filepath.Join(e.Home, ".claude", "skills", name)); o.Status != "skipped" {
		t.Fatalf("link op = %+v", o)
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "SKILL.md")); string(b) != "changed meanwhile" {
		t.Fatal("the changed copy was overwritten")
	}
}

func TestAgentsFlagAndLocalScope(t *testing.T) {
	e := env(t)
	p, err := Make(e, Options{Agents: []string{"gemini", Universal}})
	if err != nil {
		t.Fatal(err)
	}
	ops := opsByPath(p.Skills[0])
	if len(ops) != 2 || ops[filepath.Join(e.Home, ".gemini", "skills", name)].Kind != Link {
		t.Fatalf("ops = %+v", ops)
	}
	p, _ = Make(e, Options{Agents: []string{}})
	if len(p.Skills[0].Ops) != 1 || p.Skills[0].Ops[0].Role != "shared" {
		t.Fatalf("universal ops = %+v", p.Skills[0].Ops)
	}
	_, err = Make(e, Options{Agents: []string{"emacs"}})
	if r, ok := headless.IsRefusal(err); !ok || r.Code != "invalid-args" {
		t.Fatalf("err = %v", err)
	}
	_, err = Make(e, Options{Skills: []string{"nope"}})
	if r, ok := headless.IsRefusal(err); !ok || r.Code != "invalid-args" {
		t.Fatalf("err = %v", err)
	}
	// CLAUDE_CONFIG_DIR relocates Claude's folder.
	cfg := t.TempDir()
	e.Getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return cfg
		}
		return ""
	}
	p, _ = Make(e, Options{})
	if _, ok := opsByPath(p.Skills[0])[filepath.Join(cfg, "skills", name)]; !ok {
		t.Fatalf("CLAUDE_CONFIG_DIR ignored: %+v", p.Skills[0].Ops)
	}
	// Local scope links only into agent folders the project already has.
	e = env(t, "claude")
	if err := os.MkdirAll(filepath.Join(e.Cwd, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err = Make(e, Options{Local: true})
	if err != nil {
		t.Fatal(err)
	}
	ops = opsByPath(p.Skills[0])
	if p.Scope != "local" || len(ops) != 2 || ops[filepath.Join(e.Cwd, ".agents", "skills", name)].Kind != Write || ops[filepath.Join(e.Cwd, ".claude", "skills", name)].Kind != Link {
		t.Fatalf("local ops = %+v", ops)
	}
}

func TestRemoveTakesOnlyOurs(t *testing.T) {
	e := env(t, "claude", "cursor")
	p, _ := Make(e, Options{})
	if Failed(Execute(e, p)) {
		t.Fatal("install failed")
	}
	edited := filepath.Join(e.Home, ".cursor", "skills", name)
	if err := os.Remove(edited); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(edited, "SKILL.md"), []byte(string(stub())+"\nmine\n"))
	res, err := Remove(e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, r := range res {
		states = append(states, r.State)
	}
	if strings.Join(states, ",") != "removed,removed,edited" {
		t.Fatalf("remove = %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(e.Home, ".agents", "skills", name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the shared copy remains")
	}
	if _, err := os.Stat(filepath.Join(edited, "SKILL.md")); err != nil {
		t.Fatal("the edited copy was removed")
	}
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
