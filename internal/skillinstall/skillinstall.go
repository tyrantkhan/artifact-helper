// Package skillinstall puts the skill this build ships into Claude Code's skills folder, and updates the copy it
// wrote. It follows Orca's skill installer in one respect that matters: only an untouched copy of ours is ever
// rewritten; anything else is left alone and reported, or replaced after a backup when forced.
package skillinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/tyrantkhan/artifact-helper/skills"
)

// Env is what the installer reads, injected so tests can drive it.
type Env struct {
	// Home is the user's home directory, the root of global installs.
	Home string
	// Cwd is the current project, the root of local installs.
	Cwd string
	// Getenv reads CLAUDE_CONFIG_DIR, which relocates Claude Code's folder.
	Getenv func(string) string
	// Now stamps backups.
	Now func() time.Time
}

// backupFolder is the folder beside the skills folder that replaced copies are moved to.
const backupFolder = ".artifact-helper-skill-backups"

// tempName is the file a stub is written to before it is renamed into place. A copy that holds one left by a
// failed run still counts as untouched.
const tempName = ".SKILL.md.tmp"

// Root is Claude Code's skills folder: the project's with local, else $CLAUDE_CONFIG_DIR/skills, else
// ~/.claude/skills.
func (e Env) Root(local bool) string {
	if local {
		return filepath.Join(e.Cwd, ".claude", "skills")
	}
	if d := e.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "skills")
	}
	return filepath.Join(e.Home, ".claude", "skills")
}

// check refuses an environment the scope cannot use: a local plan needs the current folder, a global one the
// home folder unless CLAUDE_CONFIG_DIR names Claude Code's.
func (e Env) check(local bool) error {
	switch {
	case e.Getenv == nil:
		return errors.New("skillinstall: incomplete environment")
	case local && e.Cwd == "":
		return errors.New("skillinstall: no current directory")
	case !local && e.Home == "" && e.Getenv("CLAUDE_CONFIG_DIR") == "":
		return errors.New("skillinstall: no home directory")
	}
	return nil
}

// Options choose what a plan covers.
type Options struct {
	// Local plans the current project instead of the home directory.
	Local bool
	// Force replaces a copy that would be left alone, after moving it to a backup.
	Force bool
}

// Kind is what an operation does at a path.
type Kind string

// The operation kinds. Keep and Leave write nothing.
const (
	Keep    Kind = "keep"
	Write   Kind = "write"
	Update  Kind = "update"
	Leave   Kind = "leave"
	Replace Kind = "replace"
)

// Reasons a path is left alone.
const (
	ReasonEdited     = "edited"
	ReasonOther      = "other"
	ReasonUnreadable = "unreadable"
)

// ReasonText says each reason in plain words.
var ReasonText = map[string]string{
	ReasonEdited:     "You've changed this copy",
	ReasonOther:      "A different skill uses this name",
	ReasonUnreadable: "The folder can't be read",
}

// What a path holds, as Observed.Kind reports it.
const (
	holdsNothing    = "absent"
	holdsLink       = "link"
	holdsFolder     = "folder"
	holdsFile       = "file"
	holdsUnreadable = "unreadable"
)

// The states of a copy found on this machine, as Installed.State reports them.
const (
	StateCurrent    = "current"
	StateOutdated   = "outdated"
	StateEdited     = "edited"
	StateOther      = "other"
	StateLink       = "link"
	StateUnreadable = "unreadable"
	StateRemoved    = "removed"
)

// The statuses Execute reports.
const (
	StatusRan     = "ran"
	StatusDone    = "done"
	StatusChanged = "changed"
	StatusFailed  = "failed"
)

// Op is one planned operation.
type Op struct {
	Kind   Kind   `json:"kind"`
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
	// Replaceable says --force may back this up and put ours in its place.
	Replaceable bool `json:"replaceable,omitempty"`
	// Observed is what the path held when planned; Execute refuses to act once it differs.
	Observed Observed `json:"observed"`
}

// Observed describes what a path holds.
type Observed struct {
	// Kind is absent, link, folder, file or unreadable.
	Kind string `json:"kind"`
	// Target is a link's target.
	Target string `json:"target,omitempty"`
	// Entries lists a folder's names.
	Entries []string `json:"entries,omitempty"`
	// Digest is the digest of a folder's SKILL.md, when it has one.
	Digest string `json:"digest,omitempty"`
}

// Entry is the plan for one skill.
type Entry struct {
	Name string `json:"name"`
	// Digest is the digest of the stub this build writes.
	Digest string `json:"digest"`
	Ops    []Op   `json:"ops"`
}

// Plan is what install would do.
type Plan struct {
	Scope  string  `json:"scope"`
	Root   string  `json:"root"`
	Skills []Entry `json:"skills"`
}

// Make plans the install that opts describes, for every skill this build ships.
func Make(env Env, opts Options) (*Plan, error) {
	if err := env.check(opts.Local); err != nil {
		return nil, err
	}
	scope := "global"
	if opts.Local {
		scope = "local"
	}
	p := &Plan{Scope: scope, Root: env.Root(opts.Local)}
	for _, s := range shipped() {
		e := Entry{Name: s.Name, Digest: skills.Digest(s.Stub)}
		e.Ops = append(e.Ops, classify(filepath.Join(p.Root, s.Name), s, opts.Force))
		p.Skills = append(p.Skills, e)
	}
	return p, nil
}

// releasedExtra adds digests to every skill's released list; tests use it to stage an older shipped stub.
var releasedExtra []string

// shipped is every skill this build ships, with the stubs released before it.
func shipped() []skills.Skill {
	all := skills.All()
	for i := range all {
		all[i].Released = append(all[i].Released, releasedExtra...)
	}
	return all
}

// judge says what the copy of skill s at path is, from what observe found there: "" when there is none.
// Install and uninstall both decide from it, so they never disagree about a folder.
func judge(path string, o Observed, s skills.Skill) string {
	switch {
	// An empty folder holds nothing to keep, such as one a failed install left behind: install writes into it.
	case o.Kind == holdsNothing, o.Kind == holdsFolder && len(o.Entries) == 0:
		return ""
	case o.Kind == holdsLink:
		return StateLink
	case o.Kind == holdsFile:
		return StateOther
	case o.Kind == holdsFolder:
		// Ours means SKILL.md alone, as a build wrote it. Anything beside it is the person's, so the folder is
		// theirs to keep: uninstall removes only what this check calls ours.
		ours := len(o.Entries) == 1 && o.Entries[0] == "SKILL.md" && slices.Contains(s.Released, o.Digest)
		switch {
		case ours && o.Digest == skills.Digest(s.Stub):
			return StateCurrent
		case ours:
			return StateOutdated
		case stubNameAt(path) == s.Name:
			return StateEdited
		}
		return StateOther
	}
	return StateUnreadable
}

// classify decides what to do at path for skill s.
func classify(path string, s skills.Skill, force bool) Op {
	op := Op{Path: path, Observed: observe(path)}
	switch judge(path, op.Observed, s) {
	case "":
		op.Kind = Write
	case StateCurrent:
		op.Kind = Keep
	case StateOutdated:
		op.Kind = Update
	case StateEdited:
		op.Kind, op.Reason, op.Replaceable = Leave, ReasonEdited, true
	case StateOther, StateLink:
		op.Kind, op.Reason, op.Replaceable = Leave, ReasonOther, true
	default:
		op.Kind, op.Reason = Leave, ReasonUnreadable
	}
	if op.Kind == Leave && op.Replaceable && force {
		op.Kind = Replace
	}
	return op
}

func stubNameAt(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return ""
	}
	return skills.StubName(b)
}

// observe reports what path holds now.
func observe(path string) Observed {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Observed{Kind: holdsNothing}
	}
	if err != nil {
		return Observed{Kind: holdsUnreadable}
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(path)
		if err != nil {
			return Observed{Kind: holdsUnreadable}
		}
		return Observed{Kind: holdsLink, Target: t}
	}
	if !st.IsDir() {
		return Observed{Kind: holdsFile}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Observed{Kind: holdsUnreadable}
	}
	o := Observed{Kind: holdsFolder}
	for _, e := range entries {
		switch e.Name() {
		case ".DS_Store", "Thumbs.db", tempName:
			continue
		}
		o.Entries = append(o.Entries, e.Name())
	}
	sort.Strings(o.Entries)
	if b, err := os.ReadFile(filepath.Join(path, "SKILL.md")); err == nil {
		o.Digest = skills.Digest(b)
	}
	return o
}

// Outcome reports what Execute did for one skill.
type Outcome struct {
	Name string `json:"name"`
	// Status is ran, or changed when the path no longer held what the plan observed, so nothing ran.
	Status string      `json:"status"`
	Ops    []OpOutcome `json:"ops"`
}

// OpOutcome reports one operation.
type OpOutcome struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	// Status is done, changed or failed.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// BackupPath is where a replaced copy went. It is set on a failure too, whenever the copy is still there.
	BackupPath string `json:"backupPath,omitempty"`
}

// Failed reports whether any operation failed or found its path changed.
func Failed(outs []Outcome) bool {
	for _, o := range outs {
		for _, op := range o.Ops {
			if op.Status == StatusFailed || op.Status == StatusChanged {
				return true
			}
		}
	}
	return false
}

// Execute carries out a plan. Each path is observed again first; one that changed since the plan is left
// untouched and reported.
func Execute(env Env, p *Plan) []Outcome {
	all := shipped()
	var outs []Outcome
	for _, e := range p.Skills {
		i := slices.IndexFunc(all, func(s skills.Skill) bool { return s.Name == e.Name })
		out := Outcome{Name: e.Name, Status: StatusRan}
		for _, op := range e.Ops {
			r := OpOutcome{Path: op.Path, Kind: op.Kind, Reason: op.Reason, Status: StatusDone}
			switch {
			case op.Kind == Keep || op.Kind == Leave:
			case i < 0:
				r.Status, r.Error = StatusFailed, fmt.Sprintf("%q is not a skill this build ships", e.Name)
			case !equalObserved(observe(op.Path), op.Observed):
				r.Status, out.Status = StatusChanged, StatusChanged
			default:
				bp, err := apply(env, p.Root, op, all[i])
				r.BackupPath = bp
				if err != nil {
					r.Status, r.Error = StatusFailed, err.Error()
				}
			}
			out.Ops = append(out.Ops, r)
		}
		outs = append(outs, out)
	}
	return outs
}

func equalObserved(a, b Observed) bool {
	return a.Kind == b.Kind && a.Target == b.Target && a.Digest == b.Digest && slices.Equal(a.Entries, b.Entries)
}

// apply performs op. It returns where a replaced copy went: on success, the backup; on a failure, the backup
// only if the copy could not be put back where it was.
func apply(env Env, root string, op Op, s skills.Skill) (string, error) {
	backup := ""
	if op.Kind == Replace {
		now := env.Now
		if now == nil {
			now = time.Now
		}
		backup = filepath.Join(filepath.Dir(root), backupFolder, filepath.Base(op.Path)+"-"+now().UTC().Format("20060102T150405Z"))
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return "", fmt.Errorf("creating the backup folder: %w", err)
		}
		if err := os.Rename(op.Path, backup); err != nil {
			return "", fmt.Errorf("moving the old copy to %s: %w", backup, err)
		}
	}
	err := writeCopy(op.Path, s.Stub)
	if err == nil || backup == "" {
		return backup, err
	}
	// Put the replaced copy back, so a failed --force leaves the folder as it found it.
	if rerr := os.RemoveAll(op.Path); rerr == nil {
		if rerr = os.Rename(backup, op.Path); rerr == nil {
			return "", fmt.Errorf("%w; the copy that was there is back in place", err)
		}
	}
	return backup, fmt.Errorf("%w; the copy that was there is in %s", err, backup)
}

// writeStub writes a file; tests swap it to make a write fail.
var writeStub = os.WriteFile

// writeCopy puts stub in dir as SKILL.md. It writes beside, then renames, so a reader never sees half a stub,
// and a write that fails leaves no temporary file behind.
func writeCopy(dir string, stub []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp := filepath.Join(dir, tempName)
	if err := writeStub(tmp, stub, 0o644); err != nil {
		return errors.Join(fmt.Errorf("writing %s: %w", tmp, err), removeIfThere(tmp))
	}
	if err := os.Rename(tmp, filepath.Join(dir, "SKILL.md")); err != nil {
		return errors.Join(fmt.Errorf("writing SKILL.md: %w", err), removeIfThere(tmp))
	}
	return nil
}

func removeIfThere(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

// Installed describes a copy of a skill found on this machine.
type Installed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// State is current (ours, this build's stub), outdated (ours, an older stub), edited, other, link or
	// unreadable; Remove reports removed.
	State string `json:"state"`
}

// scan lists the copy of each skill at the root, when there is one.
func scan(env Env, local bool) ([]Installed, error) {
	if err := env.check(local); err != nil {
		return nil, err
	}
	var out []Installed
	for _, s := range shipped() {
		path := filepath.Join(env.Root(local), s.Name)
		if st := judge(path, observe(path), s); st != "" {
			out = append(out, Installed{Name: s.Name, Path: path, State: st})
		}
	}
	return out, nil
}

// Remove takes out the copies install wrote: untouched copies of ours. Anything else is left and reported
// with its state.
func Remove(env Env, opts Options) ([]Installed, error) {
	found, err := scan(env, opts.Local)
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, f := range found {
		if f.State == StateCurrent || f.State == StateOutdated {
			if err := os.RemoveAll(f.Path); err != nil {
				return out, fmt.Errorf("removing %s: %w", f.Path, err)
			}
			f.State = StateRemoved
		}
		out = append(out, f)
	}
	return out, nil
}
