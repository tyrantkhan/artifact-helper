// Package skillinstall puts the skill this build ships into Claude Code's skills folder, and updates the copy it
// wrote. It follows Orca's skill installer in one respect that matters: only an untouched copy of ours is ever
// rewritten; anything else is left alone and reported, or replaced after a backup when forced.
package skillinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tyrantkhan/artifact-helper/internal/headless"
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

// BackupFolder is the folder beside the skills folder that replaced copies are moved to.
const BackupFolder = ".artifact-helper-skill-backups"

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

// Options choose what a plan covers.
type Options struct {
	// Skills names the skills to act on. Empty means every skill this build ships.
	Skills []string
	// Local plans the current project instead of the home directory.
	Local bool
	// Update only acts where the skill is already present; it never adds a copy.
	Update bool
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

// Plan is what install or update would do.
type Plan struct {
	Scope  string  `json:"scope"`
	Root   string  `json:"root"`
	Skills []Entry `json:"skills"`
	// Digest identifies the plan; Execute runs only a plan whose paths still hold what it observed.
	Digest string `json:"planDigest"`
}

// Make plans the install or update that opts describes.
func Make(env Env, opts Options) (*Plan, error) {
	if env.Home == "" || env.Getenv == nil {
		return nil, errors.New("skillinstall: incomplete environment")
	}
	if opts.Local && env.Cwd == "" {
		return nil, errors.New("skillinstall: no current directory")
	}
	chosen, err := chooseSkills(opts.Skills)
	if err != nil {
		return nil, err
	}
	scope := "global"
	if opts.Local {
		scope = "local"
	}
	p := &Plan{Scope: scope, Root: env.Root(opts.Local)}
	for _, s := range chosen {
		e := Entry{Name: s.Name, Digest: skills.Digest(s.Stub)}
		op := classify(filepath.Join(p.Root, s.Name), s, opts)
		if !opts.Update || op.Observed.Kind != "absent" {
			e.Ops = append(e.Ops, op)
		}
		p.Skills = append(p.Skills, e)
	}
	p.Digest = digest(p)
	return p, nil
}

// releasedExtra adds digests to every skill's released list; tests use it to stage an older shipped stub.
var releasedExtra []string

func chooseSkills(names []string) ([]skills.Skill, error) {
	all := skills.All()
	for i := range all {
		all[i].Released = append(all[i].Released, releasedExtra...)
	}
	if len(names) == 0 {
		return all, nil
	}
	var out []skills.Skill
	for _, n := range names {
		i := slices.IndexFunc(all, func(s skills.Skill) bool { return s.Name == n })
		if i < 0 {
			var have []string
			for _, s := range all {
				have = append(have, s.Name)
			}
			return nil, &headless.Refusal{Code: "invalid-args", Message: fmt.Sprintf("%q is not a skill this build ships", n),
				Fix: "pass one of " + strings.Join(have, ", ")}
		}
		if !slices.ContainsFunc(out, func(x skills.Skill) bool { return x.Name == n }) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// classify decides what to do at path for skill s.
func classify(path string, s skills.Skill, opts Options) Op {
	op := Op{Path: path, Observed: observe(path)}
	current := skills.Digest(s.Stub)
	switch op.Observed.Kind {
	case "absent":
		op.Kind = Write
	case "folder":
		ours := len(op.Observed.Entries) == 1 && op.Observed.Entries[0] == "SKILL.md" && slices.Contains(s.Released, op.Observed.Digest)
		switch {
		case ours && op.Observed.Digest == current:
			op.Kind = Keep
		case ours:
			op.Kind = Update
		default:
			reason := ReasonOther
			if len(op.Observed.Entries) == 1 && op.Observed.Entries[0] == "SKILL.md" && stubNameAt(path) == s.Name {
				reason = ReasonEdited
			}
			op.Kind, op.Reason, op.Replaceable = Leave, reason, true
		}
	case "link", "file":
		op.Kind, op.Reason, op.Replaceable = Leave, ReasonOther, true
	default:
		op.Kind, op.Reason = Leave, ReasonUnreadable
	}
	if op.Kind == Leave && op.Replaceable && opts.Force {
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
		return Observed{Kind: "absent"}
	}
	if err != nil {
		return Observed{Kind: "unreadable"}
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(path)
		if err != nil {
			return Observed{Kind: "unreadable"}
		}
		return Observed{Kind: "link", Target: t}
	}
	if !st.IsDir() {
		return Observed{Kind: "file"}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Observed{Kind: "unreadable"}
	}
	o := Observed{Kind: "folder"}
	for _, e := range entries {
		if e.Name() == ".DS_Store" || e.Name() == "Thumbs.db" {
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

func digest(p *Plan) string {
	b, _ := json.Marshal(p.Skills)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
	BackupPath string `json:"backupPath,omitempty"`
}

// Failed reports whether any operation failed or found its path changed.
func Failed(outs []Outcome) bool {
	for _, o := range outs {
		for _, op := range o.Ops {
			if op.Status == "failed" || op.Status == "changed" {
				return true
			}
		}
	}
	return false
}

// Execute carries out a plan. Each path is observed again first; one that changed since the plan is left
// untouched and reported.
func Execute(env Env, p *Plan) []Outcome {
	var outs []Outcome
	for _, e := range p.Skills {
		chosen, _ := chooseSkills([]string{e.Name})
		s := chosen[0]
		out := Outcome{Name: e.Name, Status: "ran"}
		for _, op := range e.Ops {
			r := OpOutcome{Path: op.Path, Kind: op.Kind, Reason: op.Reason, Status: "done"}
			if op.Kind != Keep && op.Kind != Leave {
				if !equalObserved(observe(op.Path), op.Observed) {
					r.Status, out.Status = "changed", "changed"
				} else if bp, err := apply(env, p.Root, op, s); err != nil {
					r.Status, r.Error = "failed", err.Error()
				} else {
					r.BackupPath = bp
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

// apply performs op and returns the backup path when it replaced a copy.
func apply(env Env, root string, op Op, s skills.Skill) (string, error) {
	backup := ""
	if op.Kind == Replace {
		backup = filepath.Join(filepath.Dir(root), BackupFolder, filepath.Base(op.Path)+"-"+env.Now().UTC().Format("20060102T150405Z"))
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return "", fmt.Errorf("creating the backup folder: %w", err)
		}
		if err := os.Rename(op.Path, backup); err != nil {
			return "", fmt.Errorf("moving the old copy to %s: %w", backup, err)
		}
	}
	if err := os.MkdirAll(op.Path, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", op.Path, err)
	}
	// Write beside, then rename, so a reader never sees a half-written stub.
	tmp := filepath.Join(op.Path, ".SKILL.md.tmp")
	if err := os.WriteFile(tmp, s.Stub, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(op.Path, "SKILL.md")); err != nil {
		return "", fmt.Errorf("writing SKILL.md: %w", err)
	}
	return backup, nil
}

// Installed describes a copy of a skill found on this machine.
type Installed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// State is current (ours, this build's stub), outdated (ours, an older stub), edited, other, link or
	// unreadable; Remove reports removed.
	State string `json:"state"`
}

// Scan lists the copy of each skill at the root, when there is one.
func Scan(env Env, local bool) ([]Installed, error) {
	if env.Home == "" || env.Getenv == nil {
		return nil, errors.New("skillinstall: incomplete environment")
	}
	all, _ := chooseSkills(nil)
	var out []Installed
	for _, s := range all {
		path := filepath.Join(env.Root(local), s.Name)
		if st := state(path, s); st != "" {
			out = append(out, Installed{Name: s.Name, Path: path, State: st})
		}
	}
	return out, nil
}

func state(path string, s skills.Skill) string {
	o := observe(path)
	switch o.Kind {
	case "absent":
		return ""
	case "link":
		return "link"
	case "folder":
		ours := len(o.Entries) == 1 && o.Entries[0] == "SKILL.md" && slices.Contains(s.Released, o.Digest)
		switch {
		case ours && o.Digest == skills.Digest(s.Stub):
			return "current"
		case ours:
			return "outdated"
		case stubNameAt(path) == s.Name:
			return "edited"
		}
		return "other"
	case "file":
		return "other"
	}
	return "unreadable"
}

// Remove takes out the copies install wrote: untouched copies of ours. Anything else is left and reported
// with its state.
func Remove(env Env, opts Options) ([]Installed, error) {
	found, err := Scan(env, opts.Local)
	if err != nil {
		return nil, err
	}
	chosen, err := chooseSkills(opts.Skills)
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, f := range found {
		if !slices.ContainsFunc(chosen, func(s skills.Skill) bool { return s.Name == f.Name }) {
			continue
		}
		if f.State == "current" || f.State == "outdated" {
			if err := os.RemoveAll(f.Path); err != nil {
				return out, fmt.Errorf("removing %s: %w", f.Path, err)
			}
			f.State = "removed"
		}
		out = append(out, f)
	}
	return out, nil
}
