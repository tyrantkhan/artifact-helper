package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tyrantkhan/artifact-helper/internal/headless"
)

// config is what init saves. Every command but the open ones refuses until it exists; environment variables
// and flags still override what it holds.
type config struct {
	// Organization is the organization claude must be signed in to; empty checks none.
	Organization string `json:"organization"`
	// ClaudeConfigDir is headless.ConfigDirDefault or the absolute path of a Claude Code config folder.
	ClaudeConfigDir string `json:"claudeConfigDir"`
}

// openCommands run before init: init itself, and get-skill, whose guide tells an agent how to run init.
var openCommands = map[string]bool{"init": true, "get-skill": true}

// configPath is where init saves the config: Env.Config, else artifact-helper/config.json in the user's
// config folder.
func configPath(env *Env) (string, error) {
	if env.Config != "" {
		return env.Config, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding the user config folder: %w", err)
	}
	return filepath.Join(dir, "artifact-helper", "config.json"), nil
}

// loadConfig reads the config at path. A missing file is nil with no error; one that cannot be read or makes
// no sense is a Refusal with code "invalid-config".
func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, &headless.Refusal{Code: "invalid-config", Message: fmt.Sprintf("reading %s: %v", path, err), Fix: "run artifact-helper init again"}
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &headless.Refusal{Code: "invalid-config", Message: fmt.Sprintf("%s is not valid JSON: %v", path, err), Fix: "run artifact-helper init again"}
	}
	if !headless.ValidConfigDir(c.ClaudeConfigDir) {
		return nil, &headless.Refusal{Code: "invalid-config",
			Message: fmt.Sprintf("%s has claudeConfigDir %q, which is neither %q nor an absolute path", path, c.ClaudeConfigDir, headless.ConfigDirDefault),
			Fix:     "run artifact-helper init again"}
	}
	return &c, nil
}

func saveConfig(path string, c config) error {
	// Two strings always encode.
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// requireConfig refuses with code "not-initialized" until init has saved a config.
func requireConfig(env *Env) error {
	path, err := configPath(env)
	if err != nil {
		return err
	}
	c, err := loadConfig(path)
	if err != nil {
		return err
	}
	if c == nil {
		return &headless.Refusal{Code: "not-initialized",
			Message: "artifact-helper is not set up on this machine: there is no " + path,
			Fix: "a person runs artifact-helper init in a terminal and answers its questions; an agent runs artifact-helper init, " +
				"shows the person the accounts it found, and with their yes runs artifact-helper init --save " +
				"(adding --plugin only if they said yes to the plugin)"}
	}
	return nil
}
