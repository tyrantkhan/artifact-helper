// Package skillinstall puts the skills this build ships where coding agents read skills, and updates the
// copies it wrote. The shape follows Orca's skill installer: one shared copy under .agents/skills, a link to
// it from each agent's own skills folder, and a plan that touches only untouched copies of ours unless forced.
package skillinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
	"github.com/tyrantkhan/claude-artifact-helper/skills"
)

// Env is what the installer reads, injected so tests can drive it.
type Env struct {
	// Home is the user's home directory, the root of global installs.
	Home string
	// Cwd is the current project, the root of local installs.
	Cwd string
	// Getenv reads environment variables, for agents that relocate their home.
	Getenv func(string) string
	// LookPath finds an agent's command line, for detection.
	LookPath func(string) (string, error)
	// Now stamps backups.
	Now func() time.Time
}

// Agent is a coding agent that reads skills from a folder of its own, or from the shared folder.
type Agent struct {
	ID   string
	Name string
	// Segments is the agent's global skills folder under home; nil means it reads the shared folder.
	Segments []string
	// Local is the agent's project skills folder under the project; nil means the shared folder.
	Local []string
	// HomeEnv, when set, names a variable that relocates the agent's home (skills live in <home>/skills).
	HomeEnv string
	// Command is looked up on PATH to detect the agent; its config folder under home counts too.
	Command string
	// Dir is the agent's config folder under home, for detection.
	Dir string
}

// Universal is the --agent value for the shared folder alone.
const Universal = "universal"

// Agents lists the agents the installer knows, in install order.
var Agents = []Agent{
	{ID: "claude", Name: "Claude Code", Segments: []string{".claude", "skills"}, Local: []string{".claude", "skills"}, HomeEnv: "CLAUDE_CONFIG_DIR", Command: "claude", Dir: ".claude"},
	{ID: "codex", Name: "Codex", Command: "codex", Dir: ".codex"},
	{ID: "cursor", Name: "Cursor", Segments: []string{".cursor", "skills"}, Command: "cursor", Dir: ".cursor"},
	{ID: "gemini", Name: "Gemini CLI", Segments: []string{".gemini", "skills"}, Command: "gemini", Dir: ".gemini"},
}

func agent(id string) (Agent, bool) {
	for _, a := range Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func (a Agent) present(env Env) bool {
	if a.Command != "" {
		if _, err := env.LookPath(a.Command); err == nil {
			return true
		}
	}
	if a.HomeEnv != "" && env.Getenv(a.HomeEnv) != "" {
		return true
	}
	_, err := os.Stat(filepath.Join(env.Home, a.Dir))
	return err == nil
}

// globalRoot is the agent's global skills folder, or "" when it reads the shared one.
func (a Agent) globalRoot(env Env) string {
	if a.Segments == nil {
		return ""
	}
	if a.HomeEnv != "" {
		if h := env.Getenv(a.HomeEnv); h != "" {
			return filepath.Join(h, "skills")
		}
	}
	return filepath.Join(append([]string{env.Home}, a.Segments...)...)
}

// localRoot is the agent's project skills folder, when the project already has the agent's folder.
func (a Agent) localRoot(env Env) string {
	if a.Local == nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, a.Local[0])); err != nil {
		return ""
	}
	return filepath.Join(append([]string{env.Cwd}, a.Local...)...)
}

// Options choose what a plan covers.
type Options struct {
	// Skills names the skills to act on. Empty means every skill this build ships.
	Skills []string
	// Agents names the agents to target. Nil detects them; an empty list is the shared folder alone.
	Agents []string
	// Local plans the current project instead of the home directory.
	Local bool
	// Update only acts where the skill is already present; it never adds a copy.
	Update bool
	// Force replaces copies that would be left alone, after moving them to a backup.
	Force bool
}

// Kind is what an operation does at a path.
type Kind string

// The operation kinds. Keep and Leave write nothing.
const (
	Keep    Kind = "keep"
	Write   Kind = "write"
	Update  Kind = "update"
	Link    Kind = "link"
	Leave   Kind = "leave"
	Replace Kind = "replace"
)

// Reasons a path is left alone.
const (
	ReasonEdited           = "edited"
	ReasonOther            = "other"
	ReasonUnreadable       = "unreadable"
	ReasonSharedNotCurrent = "shared-not-current"
)

// ReasonText says each reason in plain words.
var ReasonText = map[string]string{
	ReasonEdited:           "You've changed this copy",
	ReasonOther:            "A different skill uses this name",
	ReasonUnreadable:       "The folder can't be read",
	ReasonSharedNotCurrent: "The shared copy isn't ours",
}

// Op is one planned operation.
type Op struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path"`
	// Role is shared for the .agents/skills copy, agent for an agent's own folder.
	Role string `json:"role"`
	// Agents names the agents that read Path.
	Agents []string `json:"agents,omitempty"`
	// Content is files for a copy, link for a symbolic link to the shared copy.
	Content    string `json:"content,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Reason     string `json:"reason,omitempty"`
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
	if env.Home == "" || env.Getenv == nil || env.LookPath == nil {
		return nil, errors.New("skillinstall: incomplete environment")
	}
	chosen, err := chooseSkills(opts.Skills)
	if err != nil {
		return nil, err
	}
	agents, err := chooseAgents(env, opts.Agents)
	if err != nil {
		return nil, err
	}
	root, scope := env.Home, "global"
	if opts.Local {
		if env.Cwd == "" {
			return nil, errors.New("skillinstall: no current directory")
		}
		root, scope = env.Cwd, "local"
	}
	p := &Plan{Scope: scope, Root: root}
	for _, s := range chosen {
		e := Entry{Name: s.Name, Digest: skills.Digest(s.Stub)}
		shared := filepath.Join(root, ".agents", "skills", s.Name)
		sharedOp := classify(shared, s, "shared", nil, true, opts)
		if sharedOp.Kind == Leave && sharedOp.Observed.Kind == "absent" {
			// Update never adds the shared copy.
			sharedOp = Op{}
		}
		sharedOurs := sharedOp.Kind != "" && sharedOp.Kind != Leave
		if sharedOp.Kind != "" {
			e.Ops = append(e.Ops, sharedOp)
		}
		// Agents reading the shared folder are listed on it.
		for _, a := range agents {
			if r := agentRoot(env, a, opts.Local); r == "" {
				if len(e.Ops) > 0 && e.Ops[0].Role == "shared" {
					e.Ops[0].Agents = append(e.Ops[0].Agents, a.ID)
				}
			}
		}
		for _, a := range agents {
			r := agentRoot(env, a, opts.Local)
			if r == "" {
				continue
			}
			path := filepath.Join(r, s.Name)
			op := classify(path, s, "agent", []string{a.ID}, false, opts)
			if op.Kind == Leave && op.Observed.Kind == "absent" {
				continue // update never adds
			}
			if op.Kind == Link || (op.Kind == Replace && op.Content == "link") || (op.Kind == Keep && op.Observed.Kind == "link") {
				switch {
				case op.Observed.Kind == "link" && samePath(op.Observed.Target, shared) && sharedOurs:
					op.Kind, op.Content, op.LinkTarget, op.Reason = Keep, "", "", ""
				case op.Observed.Kind == "link" && samePath(op.Observed.Target, shared):
					op = Op{Kind: Leave, Path: path, Role: "agent", Agents: []string{a.ID}, Reason: ReasonSharedNotCurrent, Observed: op.Observed}
				case !sharedOurs:
					// The shared folder is somebody else's: give this agent its own copy instead of a link.
					op.Content, op.LinkTarget = "files", ""
					if op.Kind == Link {
						op.Kind = Write
						if op.Observed.Kind == "folder" {
							op.Kind = Update
						}
					}
				default:
					op.LinkTarget = shared
				}
			}
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
		s, ok := skills.Find(n)
		if !ok {
			var have []string
			for _, s := range all {
				have = append(have, s.Name)
			}
			return nil, &headless.Refusal{Code: "invalid-args", Message: fmt.Sprintf("%q is not a skill this build ships", n),
				Fix: "pass one of " + strings.Join(have, ", ")}
		}
		if !slices.ContainsFunc(out, func(x skills.Skill) bool { return x.Name == s.Name }) {
			out = append(out, s)
		}
	}
	return out, nil
}

// chooseAgents resolves --agent: nil detects, Universal alone means the shared folder only.
func chooseAgents(env Env, names []string) ([]Agent, error) {
	if names == nil {
		var out []Agent
		for _, a := range Agents {
			if a.present(env) {
				out = append(out, a)
			}
		}
		return out, nil
	}
	var out []Agent
	for _, n := range names {
		if n == Universal {
			continue
		}
		a, ok := agent(n)
		if !ok {
			var ids []string
			for _, a := range Agents {
				ids = append(ids, a.ID)
			}
			return nil, &headless.Refusal{Code: "invalid-args", Message: fmt.Sprintf("%q is not an agent", n),
				Fix: "pass one or more of " + strings.Join(ids, ", ") + ", or " + Universal + " for the shared folder alone"}
		}
		if !slices.ContainsFunc(out, func(x Agent) bool { return x.ID == a.ID }) {
			out = append(out, a)
		}
	}
	return out, nil
}

func agentRoot(env Env, a Agent, local bool) string {
	if local {
		return a.localRoot(env)
	}
	return a.globalRoot(env)
}

// classify decides what to do at path for skill s. shared says whether path is the shared copy (which gets
// files) or an agent folder (which gets a link to it, or files on Windows).
func classify(path string, s skills.Skill, role string, agents []string, shared bool, opts Options) Op {
	op := Op{Path: path, Role: role, Agents: agents, Observed: observe(path)}
	want := Write
	content := "files"
	if !shared && runtime.GOOS != "windows" {
		want, content = Link, "link"
	}
	current := skills.Digest(s.Stub)
	switch op.Observed.Kind {
	case "absent":
		if opts.Update {
			op.Kind = Leave
			return op
		}
		op.Kind, op.Content = want, content
	case "link":
		if shared {
			op.Kind, op.Reason, op.Replaceable = Leave, ReasonOther, true
		} else {
			op.Kind, op.Content = want, content // Make settles links against the shared path
		}
	case "folder":
		ours := len(op.Observed.Entries) == 1 && op.Observed.Entries[0] == "SKILL.md" && slices.Contains(s.Released, op.Observed.Digest)
		switch {
		case ours && shared && op.Observed.Digest == current:
			op.Kind = Keep
		case ours && shared:
			op.Kind, op.Content = Update, "files"
		case ours:
			// An agent folder holding an untouched copy becomes a link to the shared one (or a fresh copy).
			op.Kind, op.Content = want, content
			if want == Write {
				if op.Observed.Digest == current {
					op.Kind = Keep
				} else {
					op.Kind = Update
				}
			}
		default:
			reason := ReasonOther
			if len(op.Observed.Entries) == 1 && op.Observed.Entries[0] == "SKILL.md" && op.Observed.Digest != "" && stubNameAt(path) == s.Name {
				reason = ReasonEdited
			}
			op.Kind, op.Reason, op.Replaceable = Leave, reason, true
		}
	case "file":
		op.Kind, op.Reason, op.Replaceable = Leave, ReasonOther, true
	default:
		op.Kind, op.Reason = Leave, ReasonUnreadable
	}
	if op.Kind == Leave && op.Replaceable && opts.Force {
		op.Kind, op.Content = Replace, content
		if shared {
			op.Content = "files"
		}
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
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(path), t)
		}
		return Observed{Kind: "link", Target: filepath.Clean(t)}
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

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return ra == rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func digest(p *Plan) string {
	b, _ := json.Marshal(p.Skills)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Outcome reports what Execute did for one skill.
type Outcome struct {
	Name string `json:"name"`
	// Status is ran, or changed when a path no longer held what the plan observed, so nothing ran there.
	Status string      `json:"status"`
	Ops    []OpOutcome `json:"ops"`
}

// OpOutcome reports one operation.
type OpOutcome struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	// Status is done, changed, skipped (its shared copy did not land) or failed.
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
		sharedOK := true
		for _, op := range e.Ops {
			r := OpOutcome{Path: op.Path, Kind: op.Kind, Reason: op.Reason, Status: "done"}
			switch {
			case op.Kind == Keep || op.Kind == Leave:
			case op.Content == "link" && !sharedOK:
				r.Status = "skipped"
			default:
				now := observe(op.Path)
				if !equalObserved(now, op.Observed) {
					r.Status = "changed"
					out.Status = "changed"
				} else if err := apply(env, op, s); err != nil {
					r.Status, r.Error = "failed", err.Error()
				} else if op.Kind == Replace {
					r.BackupPath = backupPathFor(env, op.Path)
				}
				if op.Role == "shared" && r.Status != "done" {
					sharedOK = false
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

// backupPathFor is deterministic for one run, so Execute can report where a replaced copy went.
func backupPathFor(env Env, path string) string {
	return filepath.Join(env.Home, ".agents", ".artifact-helper-skill-backups",
		filepath.Base(path)+"-"+env.Now().UTC().Format("20060102T150405Z")+"-"+shortHash(path))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func apply(env Env, op Op, s skills.Skill) error {
	if op.Kind == Replace {
		bp := backupPathFor(env, op.Path)
		if err := os.MkdirAll(filepath.Dir(bp), 0o755); err != nil {
			return fmt.Errorf("creating the backup folder: %w", err)
		}
		if err := os.Rename(op.Path, bp); err != nil {
			return fmt.Errorf("moving the old copy to %s: %w", bp, err)
		}
	}
	if op.Content == "link" {
		if op.Observed.Kind == "folder" || op.Observed.Kind == "link" {
			if err := os.RemoveAll(op.Path); err != nil {
				return fmt.Errorf("removing the old copy: %w", err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(op.Path), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(op.Path), err)
		}
		if err := os.Symlink(op.LinkTarget, op.Path); err != nil {
			return fmt.Errorf("linking: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(op.Path, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", op.Path, err)
	}
	// Write beside, then rename, so a reader never sees a half-written stub.
	tmp := filepath.Join(op.Path, ".SKILL.md.tmp")
	if err := os.WriteFile(tmp, s.Stub, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(op.Path, "SKILL.md")); err != nil {
		return fmt.Errorf("writing SKILL.md: %w", err)
	}
	return nil
}

// Installed describes a copy of a skill found on this machine.
type Installed struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Agents names who reads it.
	Agents []string `json:"agents,omitempty"`
	// State is current (ours, this build's stub), outdated (ours, an older stub), edited, other, link (to the
	// shared copy) or foreign-link.
	State string `json:"state"`
}

// Scan lists the copies of each skill at the global roots, and the project's with local.
func Scan(env Env, local bool) ([]Installed, error) {
	if env.Home == "" || env.Getenv == nil || env.LookPath == nil {
		return nil, errors.New("skillinstall: incomplete environment")
	}
	root := env.Home
	if local {
		root = env.Cwd
	}
	var out []Installed
	all, _ := chooseSkills(nil)
	for _, s := range all {
		current := skills.Digest(s.Stub)
		shared := filepath.Join(root, ".agents", "skills", s.Name)
		var sharedAgents []string
		for _, a := range Agents {
			if agentRoot(env, a, local) == "" {
				sharedAgents = append(sharedAgents, a.ID)
			}
		}
		if st := state(shared, s, current, shared); st != "" {
			out = append(out, Installed{Name: s.Name, Path: shared, Agents: sharedAgents, State: st})
		}
		for _, a := range Agents {
			r := agentRoot(env, a, local)
			if r == "" {
				continue
			}
			p := filepath.Join(r, s.Name)
			if st := state(p, s, current, shared); st != "" {
				out = append(out, Installed{Name: s.Name, Path: p, Agents: []string{a.ID}, State: st})
			}
		}
	}
	return out, nil
}

func state(path string, s skills.Skill, current, shared string) string {
	o := observe(path)
	switch o.Kind {
	case "absent":
		return ""
	case "link":
		if samePath(o.Target, shared) {
			return "link"
		}
		return "foreign-link"
	case "folder":
		ours := len(o.Entries) == 1 && o.Entries[0] == "SKILL.md" && slices.Contains(s.Released, o.Digest)
		switch {
		case ours && o.Digest == current:
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

// Remove takes out the copies install wrote: links to the shared copy, and untouched copies of ours. Anything
// else is left and reported with its state.
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
	// Links first, then the shared copy they point at.
	sort.SliceStable(found, func(i, j int) bool { return found[i].State == "link" && found[j].State != "link" })
	for _, f := range found {
		if !slices.ContainsFunc(chosen, func(s skills.Skill) bool { return s.Name == f.Name }) {
			continue
		}
		switch f.State {
		case "link", "current", "outdated":
			if err := os.RemoveAll(f.Path); err != nil {
				return out, fmt.Errorf("removing %s: %w", f.Path, err)
			}
			f.State = "removed"
		}
		out = append(out, f)
	}
	return out, nil
}
