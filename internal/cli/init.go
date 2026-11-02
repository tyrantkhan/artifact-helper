package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tyrantkhan/artifact-helper/internal/headless"
)

const initHelp = `Usage: artifact-helper init [--save] [--organization <name>] [--claude-config-dir default|<path>] [--plugin]

Set up artifact-helper on this machine. Every command but help, --version, get-skill and init refuses with
code not-initialized until init has saved its config.

init finds the Claude account claude runs as with its own config folder (default), and with the
CLAUDE_CONFIG_DIR this shell sets, when it sets one to an absolute path. It saves the folder to use and
that account's organization, which every command then checks, to config.json in the user config folder
(~/Library/Application Support/artifact-helper on macOS, ~/.config/artifact-helper on Linux,
%AppData%\artifact-helper on Windows). Once saved, that folder applies to every command, whatever
CLAUDE_CONFIG_DIR the calling session sets.

In a terminal, init asks which folder to use, whether to save, and whether to install the plugin. Without
one, it saves only with --save and installs only with --plugin. An agent runs init first, shows the person
what it found, and passes --save or --plugin only with their yes.

Flags:
  --save                   save the config
  --organization <name>    the organization to require (default: the chosen account's)
  --claude-config-dir <d>  default, or the absolute path of a Claude Code config folder
                           (default: ARTIFACT_HELPER_CLAUDE_CONFIG_DIR when set, else default)
  --plugin                 also install the Claude Code plugin into the Claude Code this shell runs:
                           claude plugin marketplace add tyrantkhan/artifact-helper, then
                           claude plugin install artifact-helper@artifact-helper

Result:
  {"ok": true, "command": "init", "saved": true, "path": "/Users/me/.config/artifact-helper/config.json",
   "organization": "Example Inc", "claudeConfigDir": "default",
   "logins": [{"claudeConfigDir": "default", "loggedIn": true, "authMethod": "claude.ai",
               "email": "me@example.com", "organization": "Example Inc"}],
   "plugin": "skipped"}

"plugin" is installed, already-installed, skipped or failed. "unusableConfigDir" names an inherited
CLAUDE_CONFIG_DIR that is not an absolute path, which init cannot offer. Without --save and without a
terminal, a chosen folder with no login shows in "logins" as "loggedIn": false and is not refused.

A refusal's code is not-logged-in when init is to save a folder with no login (with --save, or in a
terminal), wrong-organization when
--organization names another organization than the account's, organization-unverifiable when the account
names none, no-answer when a question got no answer, plugin-failed, or one doctor gives. Once the logins
are known, a refusal still carries the result's fields, with "saved" saying whether the config was saved.
`

// login is who claude runs as with one config folder.
type login struct {
	ClaudeConfigDir string `json:"claudeConfigDir"`
	LoggedIn        bool   `json:"loggedIn"`
	AuthMethod      string `json:"authMethod,omitempty"`
	Email           string `json:"email,omitempty"`
	Organization    string `json:"organization,omitempty"`
}

func (l login) account() string {
	switch {
	case !l.LoggedIn:
		return "not signed in"
	case l.Email != "" && l.Organization != "":
		return l.Email + " in " + l.Organization
	case l.Email != "":
		return l.Email + ", no organization"
	case l.Organization != "":
		return l.Organization
	}
	return "an account claude does not name (" + l.AuthMethod + ")"
}

type initResult struct {
	Saved           bool    `json:"saved"`
	Path            string  `json:"path"`
	Organization    string  `json:"organization"`
	ClaudeConfigDir string  `json:"claudeConfigDir"`
	Logins          []login `json:"logins"`
	// UnusableConfigDir is an inherited CLAUDE_CONFIG_DIR that is not an absolute path, which init cannot save.
	UnusableConfigDir string `json:"unusableConfigDir,omitempty"`
	Plugin            string `json:"plugin"`
}

func cmdInit(env *Env, args []string) error {
	opts, err := envOptions(env)
	if err != nil {
		return err
	}
	// init decides the organization and the folder itself; only its own flags override what it finds.
	envDir := opts.ConfigDir
	opts.Organization, opts.ConfigDir = "", ""
	fs := newFlags(env, "init", initHelp)
	addOptionFlags(fs, env, &opts)
	save := fs.Bool("save", false, "save the config")
	plugin := fs.Bool("plugin", false, "also install the Claude Code plugin")
	var flagDir string
	fs.Func("claude-config-dir", "default, or the absolute path of a Claude Code config folder", func(v string) error {
		v = strings.TrimSpace(v)
		if !headless.ValidConfigDir(v) {
			return fmt.Errorf("%q is neither %s nor an absolute path", v, headless.ConfigDirDefault)
		}
		flagDir = v
		return nil
	})
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return badArgs("init", "init takes no arguments")
	}
	path, err := configPath(env)
	if err != nil {
		return err
	}
	interactive := isTerminal(env, env.Stdin) && isTerminal(env, env.Stdout)
	u := newUI(env)
	if interactive {
		u.title("artifact-helper init")
	}
	res, err := runInit(u, interactive, opts, path, envDir, flagDir, *save, *plugin)
	if interactive {
		if res != nil {
			fmt.Fprintln(u.w)
			if res.Saved {
				u.row(u.mark(true), "saved", path)
			} else {
				u.row(u.mark(false), "not saved", u.dim("commands keep refusing until init saves"))
			}
			switch res.Plugin {
			case "installed":
				u.row(u.mark(true), "plugin", headless.PluginID+" installed")
			case "already-installed":
				u.row(u.mark(true), "plugin", headless.PluginID+" was already installed")
			case "failed":
				u.row(u.mark(false), "plugin", "not installed")
			}
		}
		if err != nil {
			u.problem(err)
			return &exitError{code: 1}
		}
		fmt.Fprintln(u.w)
		return nil
	}
	switch {
	case err == nil:
		return writeJSON(env, "init", res)
	case res == nil:
		return err
	}
	// A refusal after the logins are known keeps them, and says whether the config was saved.
	rf, ok := headless.IsRefusal(err)
	if !ok {
		rf = &headless.Refusal{Code: "error", Message: err.Error()}
		fmt.Fprintln(env.Stderr, "artifact-helper init: "+err.Error())
	}
	b, merr := json.Marshal(res)
	out := map[string]any{}
	if merr == nil {
		merr = json.Unmarshal(b, &out)
	}
	if merr != nil {
		return fmt.Errorf("encoding result: %w", merr)
	}
	out["ok"], out["command"], out["error"] = false, "init", rf
	b, merr = json.MarshalIndent(out, "", "  ")
	if merr != nil {
		return fmt.Errorf("encoding result: %w", merr)
	}
	fmt.Fprintln(env.Stdout, string(b))
	return &exitError{code: 1}
}

// runInit finds the logins, settles the folder and organization, and saves and installs as asked. Once the
// logins are known it returns the result even with an error, so the caller can still show them.
func runInit(u *ui, interactive bool, opts headless.Options, path, envDir, flagDir string, save, plugin bool) (*initResult, error) {
	dirs := []string{headless.ConfigDirDefault}
	// Only a folder init can save is offered: Claude Code takes a relative CLAUDE_CONFIG_DIR, but every later
	// run would start in another directory.
	inherited := headless.InheritedConfigDir(opts.Environ)
	unusable := ""
	if inherited != "" && !headless.ValidConfigDir(inherited) {
		unusable, inherited = inherited, ""
	}
	for _, d := range []string{inherited, envDir, flagDir} {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	logins := make([]login, len(dirs))
	for i, d := range dirs {
		var err error
		if logins[i], err = probe(ctx, opts, d); err != nil {
			return nil, err
		}
	}

	res := &initResult{Path: path, Logins: logins, UnusableConfigDir: unusable, Plugin: "skipped"}

	if interactive {
		fmt.Fprintf(u.w, "  %s\n", u.bold("Claude accounts on this machine"))
		for i, l := range logins {
			fmt.Fprintf(u.w, "    %s  %-30s %s\n", u.cyan(fmt.Sprint(i+1)), l.ClaudeConfigDir, l.account())
		}
		if unusable != "" {
			fmt.Fprintf(u.w, "    %s\n", u.dim("CLAUDE_CONFIG_DIR="+unusable+" is not an absolute path, so init cannot offer it"))
		}
		fmt.Fprintln(u.w)
	}
	choice := flagDir
	if choice == "" {
		choice = envDir
	}
	if choice == "" {
		choice = headless.ConfigDirDefault
		if interactive && len(dirs) > 1 {
			i, err := u.choose("Use which?", len(dirs), 1)
			if err != nil {
				return res, err
			}
			choice = dirs[i-1]
		}
	}
	res.ClaudeConfigDir = choice
	chosen := logins[slices.Index(dirs, choice)]
	// Only saving needs the chosen folder signed in. Without --save, the agent still gets the logins to ask
	// the person which folder to use, and the plugin, which does not depend on that folder, still installs.
	if !chosen.LoggedIn && (save || interactive) {
		return res, &headless.Refusal{Code: "not-logged-in", Message: "claude is not signed in with config folder " + choice,
			Fix: "run `claude auth login` (with CLAUDE_CONFIG_DIR set to that folder unless it is default), or pick another folder with --claude-config-dir"}
	}
	res.Organization = chosen.Organization
	if opts.Organization != "" && chosen.LoggedIn {
		if chosen.Organization == "" {
			return res, &headless.Refusal{Code: "organization-unverifiable",
				Message: fmt.Sprintf("--organization is %s, but config folder %s is signed in as %s, which names no organization, so it could never be checked", opts.Organization, choice, chosen.account()),
				Fix:     "drop --organization, or sign in with `claude auth login` so claude reports the organization"}
		}
		if !strings.EqualFold(opts.Organization, chosen.Organization) {
			return res, &headless.Refusal{Code: "wrong-organization",
				Message: fmt.Sprintf("--organization is %s, but config folder %s is signed in as %s", opts.Organization, choice, chosen.account()),
				Fix:     "drop --organization to take the account's, or sign in with an account in " + opts.Organization}
		}
	}

	doSave := save
	if interactive {
		fmt.Fprintf(u.w, "  %s %s\n", u.bold("Saves to"), path)
		u.row(u.bullet(), "config dir", choice)
		if res.Organization != "" {
			u.row(u.bullet(), "organization", res.Organization+" "+u.dim("(every command checks it)"))
		} else {
			u.row(u.bullet(), "organization", u.dim("none: this login names no organization, so nothing is checked"))
		}
		fmt.Fprintln(u.w)
		if !doSave {
			var err error
			if doSave, err = u.confirm("Save?", true); err != nil {
				return res, err
			}
		}
	}
	if doSave {
		if err := saveConfig(path, config{Organization: res.Organization, ClaudeConfigDir: choice}); err != nil {
			return res, err
		}
		res.Saved = true
	}

	doPlugin := plugin
	if interactive && !doPlugin && res.Saved {
		var err error
		// The plugin is opt-in, so input that ends here declines it rather than failing a saved setup.
		if doPlugin, err = u.confirm("Install the Claude Code plugin?", false); err != nil && !errors.Is(err, errNoAnswer) {
			return res, err
		}
	}
	if doPlugin {
		already, err := headless.InstallPlugin(ctx, opts)
		if err != nil {
			res.Plugin = "failed"
			return res, err
		}
		res.Plugin = "installed"
		if already {
			res.Plugin = "already-installed"
		}
	}
	return res, nil
}

// probe asks claude who it runs as with config folder dir. Only not being signed in is an answer; any other
// failure stops init.
func probe(ctx context.Context, opts headless.Options, dir string) (login, error) {
	opts.ConfigDir, opts.Organization = dir, ""
	h, err := headless.Doctor(ctx, opts)
	if rf, ok := headless.IsRefusal(err); err != nil && !(ok && rf.Code == "not-logged-in" && h != nil) {
		return login{}, err
	}
	return login{ClaudeConfigDir: dir, LoggedIn: h.LoggedIn, AuthMethod: h.AuthMethod, Email: h.Email, Organization: h.Organization}, nil
}
