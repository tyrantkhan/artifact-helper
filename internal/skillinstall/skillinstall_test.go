package skillinstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
	"github.com/tyrantkhan/claude-artifact-helper/skills"
)

const name = "artifact-helper"

func env(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	return Env{Home: home, Cwd: filepath.Join(home, "project"), Getenv: func(string) string { return "" },
		Now: func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }}
}

func stub() []byte { s, _ := skills.Find(name); return s.Stub }

func only(t *testing.T, p *Plan) Op {
	t.Helper()
	if len(p.Skills) != 1 || len(p.Skills[0].Ops) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	return p.Skills[0].Ops[0]
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

func TestInstallWritesThenKeeps(t *testing.T) {
	e := env(t)
	p, err := Make(e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.Home, ".claude", "skills", name)
	if op := only(t, p); op.Kind != Write || op.Path != dir || p.Scope != "global" || p.Digest == "" {
		t.Fatalf("plan = %+v", p)
	}
	if outs := Execute(e, p); Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err != nil || string(b) != string(stub()) {
		t.Fatalf("copy: %v", err)
	}
	p, _ = Make(e, Options{})
	if op := only(t, p); op.Kind != Keep {
		t.Fatalf("second plan = %+v", op)
	}
	found, _ := Scan(e, false)
	if len(found) != 1 || found[0].State != "current" || found[0].Path != dir {
		t.Fatalf("scan = %+v", found)
	}
	// CLAUDE_CONFIG_DIR relocates the folder; --local uses the project's.
	cfg := t.TempDir()
	e.Getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return cfg
		}
		return ""
	}
	if p, _ = Make(e, Options{}); only(t, p).Path != filepath.Join(cfg, "skills", name) {
		t.Fatalf("CLAUDE_CONFIG_DIR ignored: %+v", p)
	}
	if p, _ = Make(e, Options{Local: true}); p.Scope != "local" || only(t, p).Path != filepath.Join(e.Cwd, ".claude", "skills", name) {
		t.Fatalf("local plan = %+v", p)
	}
	if _, err := Make(e, Options{Skills: []string{"nope"}}); err == nil {
		t.Fatal("an unknown skill was planned")
	}
}

func TestUpdateRewritesOnlyUntouchedCopies(t *testing.T) {
	e := env(t)
	dir := filepath.Join(e.Home, ".claude", "skills", name)
	older := []byte(strings.Replace(string(stub()), "# artifact-helper", "# artifact-helper (old)", 1))
	releasedExtra = []string{skills.Digest(older)}
	t.Cleanup(func() { releasedExtra = nil })
	// Update never adds.
	p, _ := Make(e, Options{Update: true})
	if len(p.Skills[0].Ops) != 0 {
		t.Fatalf("update planned %+v with nothing installed", p.Skills[0].Ops)
	}
	write(t, filepath.Join(dir, "SKILL.md"), older)
	p, _ = Make(e, Options{Update: true})
	if op := only(t, p); op.Kind != Update {
		t.Fatalf("op = %+v", op)
	}
	if found, _ := Scan(e, false); found[0].State != "outdated" {
		t.Fatalf("scan = %+v", found)
	}
	if outs := Execute(e, p); Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != string(stub()) {
		t.Fatal("the old copy was not updated")
	}
}

func TestEditedAndForeignCopiesAreLeftAloneUnlessForced(t *testing.T) {
	e := env(t)
	dir := filepath.Join(e.Home, ".claude", "skills", name)
	edited := []byte(string(stub()) + "\nMy own note.\n")
	write(t, filepath.Join(dir, "SKILL.md"), edited)
	p, _ := Make(e, Options{})
	if op := only(t, p); op.Kind != Leave || op.Reason != ReasonEdited || !op.Replaceable {
		t.Fatalf("op = %+v", op)
	}
	outs := Execute(e, p)
	if Failed(outs) || outs[0].Ops[0].Status != "done" {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != string(edited) {
		t.Fatal("the edited copy was touched")
	}
	if found, _ := Scan(e, false); found[0].State != "edited" {
		t.Fatalf("scan = %+v", found)
	}

	write(t, filepath.Join(dir, "SKILL.md"), []byte("---\nname: something-else\n---\n# other\n"))
	write(t, filepath.Join(dir, "script.sh"), []byte("#!/bin/sh\n"))
	p, _ = Make(e, Options{})
	if op := only(t, p); op.Kind != Leave || op.Reason != ReasonOther {
		t.Fatalf("op = %+v", op)
	}
	p, _ = Make(e, Options{Force: true})
	if op := only(t, p); op.Kind != Replace {
		t.Fatalf("forced op = %+v", op)
	}
	outs = Execute(e, p)
	if Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	bp := outs[0].Ops[0].BackupPath
	if !strings.HasPrefix(bp, filepath.Join(e.Home, ".claude", BackupFolder)) {
		t.Fatalf("backup at %q", bp)
	}
	if b, _ := os.ReadFile(filepath.Join(bp, "script.sh")); string(b) != "#!/bin/sh\n" {
		t.Fatal("the backup lacks the foreign files")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != string(stub()) {
		t.Fatal("the copy was not replaced")
	}
}

func TestExecuteRefusesAChangedPath(t *testing.T) {
	e := env(t)
	p, _ := Make(e, Options{})
	dir := filepath.Join(e.Home, ".claude", "skills", name)
	write(t, filepath.Join(dir, "SKILL.md"), []byte("changed meanwhile"))
	outs := Execute(e, p)
	if !Failed(outs) || outs[0].Status != "changed" || outs[0].Ops[0].Status != "changed" {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != "changed meanwhile" {
		t.Fatal("the changed copy was overwritten")
	}
}

func TestRemoveTakesOnlyOurs(t *testing.T) {
	e := env(t)
	p, _ := Make(e, Options{})
	if Failed(Execute(e, p)) {
		t.Fatal("install failed")
	}
	res, err := Remove(e, Options{})
	if err != nil || len(res) != 1 || res[0].State != "removed" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Lstat(filepath.Join(e.Home, ".claude", "skills", name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the copy remains")
	}
	write(t, filepath.Join(e.Home, ".claude", "skills", name, "SKILL.md"), []byte(string(stub())+"\nmine\n"))
	res, _ = Remove(e, Options{})
	if len(res) != 1 || res[0].State != "edited" {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".claude", "skills", name, "SKILL.md")); err != nil {
		t.Fatal("the edited copy was removed")
	}
	_, err = Remove(e, Options{Skills: []string{"nope"}})
	if r, ok := headless.IsRefusal(err); !ok || r.Code != "invalid-args" {
		t.Fatalf("err = %v", err)
	}
}
