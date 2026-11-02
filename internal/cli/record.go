package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tyrantkhan/artifact-helper/internal/headless"
)

// published is the record of the artifacts this machine published. It is what lets the helper, not its
// caller, own each page's link: a page published again goes to its recorded link, and a link the page moved
// away from forwards to where it lives now.
type published struct {
	Artifacts []*tracked `json:"artifacts"`
}

// tracked is one artifact this machine published.
type tracked struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	// File is the local page last published to it.
	File string `json:"file,omitempty"`
	// Sessions are the Claude Code sessions that published or edited it, by CLAUDE_CODE_SESSION_ID.
	Sessions       []string `json:"sessions,omitempty"`
	OrganizationID string   `json:"organizationId,omitempty"`
	// Version is the live version this machine last published or read. A publish goes only over it.
	Version     string `json:"version,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	// Aliases are links the page lived at before, which forward to URL.
	Aliases []string `json:"aliases,omitempty"`
	// CheckedAt is when updates last looked at it, and Seen the comment ids it found then.
	CheckedAt string   `json:"checkedAt,omitempty"`
	Seen      []string `json:"seen,omitempty"`
}

// recordPath is where the record lives: published.json beside the config.
func recordPath(env *Env) (string, error) {
	p, err := configPath(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "published.json"), nil
}

func loadRecord(env *Env) (*published, error) {
	path, err := recordPath(env)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &published{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var p published
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, &headless.Refusal{Code: "invalid-config", Message: fmt.Sprintf("%s is not valid JSON: %v", path, err),
			Fix: "tell the person; the record of published artifacts is damaged, and moving it aside starts a new one"}
	}
	return &p, nil
}

// changeRecord reads the record afresh, applies change and saves it, so two sessions publishing at once lose
// as little as one save can. The file is replaced whole, never left half written.
func changeRecord(env *Env, change func(*published)) error {
	p, err := loadRecord(env)
	if err != nil {
		return err
	}
	change(p)
	path, err := recordPath(env)
	if err != nil {
		return err
	}
	// Strings and slices of them always encode.
	data, _ := json.MarshalIndent(p, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	return nil
}

// byFile is the artifact the page at file was last published to.
func (p *published) byFile(file string) *tracked {
	for _, t := range p.Artifacts {
		if t.File == file {
			return t
		}
	}
	return nil
}

// byURL is the artifact at url, whether url is its link now or one it moved away from.
func (p *published) byURL(url string) *tracked {
	for _, t := range p.Artifacts {
		if t.URL == url || slices.Contains(t.Aliases, url) {
			return t
		}
	}
	return nil
}

// forward is the link url's artifact lives at now, and whether that differs from url.
func (p *published) forward(url string) (string, bool) {
	if t := p.byURL(url); t != nil && t.URL != url {
		return t.URL, true
	}
	return url, false
}

// recordPublish notes that file was published as res. A page that moved to a new link takes its old links
// along as aliases, so they forward.
func (p *published) recordPublish(file string, res *headless.Result, session, organizationID string, now time.Time) {
	t := p.byURL(res.URL)
	if t == nil {
		t = &tracked{URL: res.URL}
		p.Artifacts = append(p.Artifacts, t)
	}
	if file != "" {
		if old := p.byFile(file); old != nil && old != t {
			t.Aliases = append(t.Aliases, old.URL)
			t.Aliases = append(t.Aliases, old.Aliases...)
			t.Sessions = append(t.Sessions, old.Sessions...)
			p.Artifacts = slices.DeleteFunc(p.Artifacts, func(x *tracked) bool { return x == old })
		}
		t.File = file
	}
	if res.Title != "" {
		t.Title = res.Title
	}
	if res.Version != "" {
		t.Version = res.Version
	}
	if session != "" && !slices.Contains(t.Sessions, session) {
		t.Sessions = append(t.Sessions, session)
	}
	if organizationID != "" {
		t.OrganizationID = organizationID
	}
	t.PublishedAt = now.UTC().Format(time.RFC3339)
}

// newerThan reports whether a version published at updatedAt is newer than version, whose id starts with the
// Unix second it was published. It is false when either is unknown.
func newerThan(updatedAt, version string) bool {
	at, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil || version == "" {
		return false
	}
	secs, err := strconv.ParseInt(strings.SplitN(version, "-", 2)[0], 10, 64)
	if err != nil {
		return false
	}
	return at.Unix() > secs
}
