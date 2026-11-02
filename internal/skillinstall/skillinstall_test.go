package skillinstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyrantkhan/artifact-helper/skills"
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

func skillDir(e Env) string { return filepath.Join(e.Home, ".claude", "skills", name) }

// failWrites makes every stub write fail partway, as a full disk would, until the test ends.
func failWrites(t *testing.T) {
	t.Helper()
	writeStub = func(path string, b []byte, mode os.FileMode) error {
		if err := os.WriteFile(path, b[:1], mode); err != nil {
			return err
		}
		return errors.New("disk full")
	}
	t.Cleanup(func() { writeStub = os.WriteFile })
}

func TestInstallWritesThenKeeps(t *testing.T) {
	e := env(t)
	p, err := Make(e, Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := skillDir(e)
	if op := only(t, p); op.Kind != Write || op.Path != dir || p.Scope != "global" {
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
	found, _ := scan(e, false)
	if len(found) != 1 || found[0].State != StateCurrent || found[0].Path != dir {
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
}

func TestAnOlderUntouchedCopyIsUpdated(t *testing.T) {
	e := env(t)
	dir := skillDir(e)
	older := []byte(strings.Replace(string(stub()), "# artifact-helper", "# artifact-helper (old)", 1))
	releasedExtra = []string{skills.Digest(older)}
	t.Cleanup(func() { releasedExtra = nil })
	write(t, filepath.Join(dir, "SKILL.md"), older)
	p, _ := Make(e, Options{})
	if op := only(t, p); op.Kind != Update {
		t.Fatalf("op = %+v", op)
	}
	if found, _ := scan(e, false); found[0].State != StateOutdated {
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
	dir := skillDir(e)
	edited := []byte(string(stub()) + "\nMy own note.\n")
	write(t, filepath.Join(dir, "SKILL.md"), edited)
	p, _ := Make(e, Options{})
	if op := only(t, p); op.Kind != Leave || op.Reason != ReasonEdited || !op.Replaceable {
		t.Fatalf("op = %+v", op)
	}
	outs := Execute(e, p)
	if Failed(outs) || outs[0].Ops[0].Status != StatusDone {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != string(edited) {
		t.Fatal("the edited copy was touched")
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
	if !strings.HasPrefix(bp, filepath.Join(e.Home, ".claude", backupFolder)) {
		t.Fatalf("backup at %q", bp)
	}
	if b, _ := os.ReadFile(filepath.Join(bp, "script.sh")); string(b) != "#!/bin/sh\n" {
		t.Fatal("the backup lacks the foreign files")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != string(stub()) {
		t.Fatal("the copy was not replaced")
	}
}

// Install and uninstall decide from the same judgment, so they never give a folder two different reasons.
func TestInstallAndUninstallAgree(t *testing.T) {
	for _, c := range []struct {
		name   string
		files  map[string]string
		reason string
		state  string
	}{
		{"our stub and a file of theirs", map[string]string{"SKILL.md": string(stub()), "notes.md": "mine"}, ReasonEdited, StateEdited},
		{"our stub changed", map[string]string{"SKILL.md": string(stub()) + "\nmine\n"}, ReasonEdited, StateEdited},
		{"another skill", map[string]string{"SKILL.md": "---\nname: other\n---\n"}, ReasonOther, StateOther},
		{"another skill with more files", map[string]string{"SKILL.md": "---\nname: other\n---\n", "run.sh": "x"}, ReasonOther, StateOther},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := env(t)
			for f, body := range c.files {
				write(t, filepath.Join(skillDir(e), f), []byte(body))
			}
			p, _ := Make(e, Options{})
			found, _ := scan(e, false)
			if op := only(t, p); op.Kind != Leave || op.Reason != c.reason || len(found) != 1 || found[0].State != c.state {
				t.Fatalf("install %+v, uninstall %+v", op, found)
			}
		})
	}
}

func TestRemoveTakesOnlyOurs(t *testing.T) {
	e := env(t)
	if Failed(Execute(e, mustMake(t, e, Options{}))) {
		t.Fatal("install failed")
	}
	res, err := Remove(e, Options{})
	if err != nil || len(res) != 1 || res[0].State != StateRemoved {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Lstat(skillDir(e)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the copy remains")
	}
	write(t, filepath.Join(skillDir(e), "SKILL.md"), []byte(string(stub())+"\nmine\n"))
	res, _ = Remove(e, Options{})
	if len(res) != 1 || res[0].State != StateEdited {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(skillDir(e), "SKILL.md")); err != nil {
		t.Fatal("the edited copy was removed")
	}
}

// The folder holds our untouched stub, but also a file of theirs: uninstall must keep both.
func TestUninstallKeepsAFolderWithTheirFiles(t *testing.T) {
	e := env(t)
	write(t, filepath.Join(skillDir(e), "SKILL.md"), stub())
	write(t, filepath.Join(skillDir(e), "notes.md"), []byte("mine"))
	res, err := Remove(e, Options{})
	if err != nil || len(res) != 1 || res[0].State == StateRemoved {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, f := range []string{"SKILL.md", "notes.md"} {
		if _, err := os.Stat(filepath.Join(skillDir(e), f)); err != nil {
			t.Fatalf("%s was removed", f)
		}
	}
}

func TestRemoveKeepsALink(t *testing.T) {
	e := env(t)
	target := t.TempDir()
	write(t, filepath.Join(target, "SKILL.md"), stub())
	if err := os.MkdirAll(filepath.Dir(skillDir(e)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, skillDir(e)); err != nil {
		t.Skip("this system cannot make a symlink here:", err)
	}
	res, err := Remove(e, Options{})
	if err != nil || len(res) != 1 || res[0].State != StateLink {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Lstat(skillDir(e)); err != nil {
		t.Fatal("the link was removed")
	}
}

// A temporary file a failed run left beside our stub does not make the copy someone else's.
func TestALeftoverTempFileStillCountsAsOurs(t *testing.T) {
	e := env(t)
	write(t, filepath.Join(skillDir(e), "SKILL.md"), stub())
	write(t, filepath.Join(skillDir(e), tempName), []byte("half"))
	if op := only(t, mustMake(t, e, Options{})); op.Kind != Keep {
		t.Fatalf("op = %+v", op)
	}
	if res, _ := Remove(e, Options{}); len(res) != 1 || res[0].State != StateRemoved {
		t.Fatalf("res = %+v", res)
	}
}

func TestAFailedWriteLeavesNoTempFile(t *testing.T) {
	e := env(t)
	p := mustMake(t, e, Options{})
	failWrites(t)
	outs := Execute(e, p)
	if !Failed(outs) || outs[0].Ops[0].Status != StatusFailed || !strings.Contains(outs[0].Ops[0].Error, "disk full") {
		t.Fatalf("outs = %+v", outs)
	}
	if _, err := os.Stat(filepath.Join(skillDir(e), tempName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary file is still there: %v", err)
	}
}

// A forced replace that fails after moving the copy aside puts it back, so nothing is lost or hidden.
func TestAFailedForcePutsTheCopyBack(t *testing.T) {
	e := env(t)
	edited := string(stub()) + "\nMy own note.\n"
	write(t, filepath.Join(skillDir(e), "SKILL.md"), []byte(edited))
	p := mustMake(t, e, Options{Force: true})
	failWrites(t)
	outs := Execute(e, p)
	r := outs[0].Ops[0]
	if r.Status != StatusFailed || !strings.Contains(r.Error, "back in place") || r.BackupPath != "" {
		t.Fatalf("outcome = %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(skillDir(e), "SKILL.md")); string(b) != edited {
		t.Fatalf("the copy was not put back: %q", b)
	}
}

func TestForceReplacesAFileAndALink(t *testing.T) {
	e := env(t)
	// A file where the skill's folder belongs is someone else's: left alone, replaced only with Force.
	write(t, skillDir(e), []byte("not a folder"))
	if op := only(t, mustMake(t, e, Options{})); op.Kind != Leave || op.Reason != ReasonOther || op.Observed.Kind != holdsFile {
		t.Fatalf("op = %+v", op)
	}
	outs := Execute(e, mustMake(t, e, Options{Force: true}))
	if Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(outs[0].Ops[0].BackupPath); string(b) != "not a folder" {
		t.Fatalf("backup holds %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(skillDir(e), "SKILL.md")); string(b) != string(stub()) {
		t.Fatal("the file was not replaced")
	}

	// A link is moved aside as a link: its target stays as it was.
	e = env(t)
	target := t.TempDir()
	write(t, filepath.Join(target, "SKILL.md"), []byte("theirs"))
	if err := os.MkdirAll(filepath.Dir(skillDir(e)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, skillDir(e)); err != nil {
		t.Skip("this system cannot make a symlink here:", err)
	}
	outs = Execute(e, mustMake(t, e, Options{Force: true}))
	if Failed(outs) {
		t.Fatalf("outs = %+v", outs)
	}
	if st, err := os.Lstat(outs[0].Ops[0].BackupPath); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the backup is not the link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(b) != "theirs" {
		t.Fatal("the link's target was touched")
	}
}

func TestALocalBackupStaysInTheProject(t *testing.T) {
	e := env(t)
	dir := filepath.Join(e.Cwd, ".claude", "skills", name)
	write(t, filepath.Join(dir, "SKILL.md"), []byte("---\nname: other\n---\n"))
	outs := Execute(e, mustMake(t, e, Options{Local: true, Force: true}))
	if Failed(outs) || !strings.HasPrefix(outs[0].Ops[0].BackupPath, filepath.Join(e.Cwd, ".claude", backupFolder)) {
		t.Fatalf("outs = %+v", outs)
	}
}

func TestExecuteRefusesAChangedPath(t *testing.T) {
	e := env(t)
	p := mustMake(t, e, Options{})
	write(t, filepath.Join(skillDir(e), "SKILL.md"), []byte("changed meanwhile"))
	outs := Execute(e, p)
	if !Failed(outs) || outs[0].Status != StatusChanged || outs[0].Ops[0].Status != StatusChanged {
		t.Fatalf("outs = %+v", outs)
	}
	if b, _ := os.ReadFile(filepath.Join(skillDir(e), "SKILL.md")); string(b) != "changed meanwhile" {
		t.Fatal("the changed copy was overwritten")
	}
}

func TestExecuteReportsASkillThisBuildDoesNotShip(t *testing.T) {
	e := env(t)
	p := &Plan{Root: t.TempDir(), Skills: []Entry{{Name: "nope", Ops: []Op{{Kind: Write, Path: filepath.Join(t.TempDir(), "nope"), Observed: Observed{Kind: holdsNothing}}}}}}
	outs := Execute(e, p)
	if !Failed(outs) || !strings.Contains(outs[0].Ops[0].Error, "not a skill this build ships") {
		t.Fatalf("outs = %+v", outs)
	}
}

func TestEachScopeNeedsOnlyWhatItUses(t *testing.T) {
	if _, err := Make(Env{}, Options{}); err == nil {
		t.Fatal("an empty environment was accepted")
	}
	e := env(t)
	e.Cwd = ""
	if _, err := Make(e, Options{Local: true}); err == nil {
		t.Fatal("a local install without a current directory was accepted")
	}
	if _, err := Remove(e, Options{Local: true}); err == nil {
		t.Fatal("a local uninstall without a current directory was accepted")
	}
	// A local install needs no home, and CLAUDE_CONFIG_DIR stands in for it.
	e = env(t)
	e.Home = ""
	if _, err := Make(e, Options{Local: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Make(e, Options{}); err == nil {
		t.Fatal("a global install without a home was accepted")
	}
	cfg := t.TempDir()
	e.Getenv = func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return cfg
		}
		return ""
	}
	if _, err := Make(e, Options{}); err != nil {
		t.Fatal(err)
	}
}

func mustMake(t *testing.T, e Env, opts Options) *Plan {
	t.Helper()
	p, err := Make(e, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
