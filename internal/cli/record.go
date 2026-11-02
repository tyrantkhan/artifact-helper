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

// published is the record of the artifacts this machine published or read. It is what lets the helper, not its
// caller, own each page's link: a page published again goes to its recorded link, and only over the version
// that page was published as or read at.
type published struct {
	Artifacts []*tracked `json:"artifacts"`
}

// tracked is one artifact this machine published or read.
type tracked struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	// Files are the local copies of the page, each with the version it was published as or read at: the version
	// a publish of that file goes over. A file belongs to one artifact at a time.
	Files map[string]string `json:"files,omitempty"`
	// Sessions are the Claude Code sessions that published or edited it, by CLAUDE_CODE_SESSION_ID.
	Sessions       []string `json:"sessions,omitempty"`
	OrganizationID string   `json:"organizationId,omitempty"`
	// Version is the newest version this machine published or read.
	Version string `json:"version,omitempty"`
	// PublishedAt is when this machine last published it; empty for an artifact it only read.
	PublishedAt string `json:"publishedAt,omitempty"`
	// MovedTo is the artifact its page was published to with --new. That page, published or edited with this
	// link, goes there.
	MovedTo string `json:"movedTo,omitempty"`
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
		return nil, &headless.Refusal{Code: "invalid-record", Message: fmt.Sprintf("%s is not valid JSON: %v", path, err),
			Fix: "tell the person; the record of published artifacts is damaged, and moving it aside starts a new one"}
	}
	return &p, nil
}

// changeRecord reads the record afresh, applies change and saves it, so two sessions publishing at once lose
// as little as one save can. Each save writes a file of its own and renames it into place, so the record is
// never left half written.
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
	// Strings, maps and slices of them always encode.
	data, _ := json.MarshalIndent(p, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "published-*.json")
	if err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			werr = errors.Join(werr, rerr)
		}
		return fmt.Errorf("saving %s: %w", path, werr)
	}
	return nil
}

// canonical is the one spelling of a file's path the record uses, whatever directory or symlink it was named
// through, such as /tmp and /private/tmp on macOS.
func canonical(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return filepath.Clean(file)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// sameLink reports whether two links name the same artifact; a link may end in a slash.
func sameLink(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }

// byURL is the artifact at url.
func (p *published) byURL(url string) *tracked {
	for _, t := range p.Artifacts {
		if sameLink(t.URL, url) {
			return t
		}
	}
	return nil
}

// byFile is the artifact the page at file was published to or read from.
func (p *published) byFile(file string) *tracked {
	file = canonical(file)
	for _, t := range p.Artifacts {
		if _, ok := t.Files[file]; ok {
			return t
		}
	}
	return nil
}

// follow is where a publish of file to url goes instead, and whether it does: the artifact url's page moved to
// with --new, when file is that page. Any other file goes to url itself, which is still live.
func (p *published) follow(url, file string) (string, bool) {
	cur := strings.TrimSuffix(url, "/")
	for seen := map[string]bool{}; !seen[cur]; {
		seen[cur] = true
		t := p.byURL(cur)
		if t == nil || t.MovedTo == "" {
			break
		}
		cur = t.MovedTo
	}
	if sameLink(cur, url) || file == "" {
		return url, false
	}
	if t := p.byFile(file); t == nil || !sameLink(t.URL, cur) {
		return url, false
	}
	return cur, true
}

// base is the version a publish of file to t goes over: the one file was published as or read at, or else the
// newest this machine has seen of t.
func (t *tracked) base(file string) string {
	if v := t.Files[canonical(file)]; v != "" {
		return v
	}
	return t.Version
}

// entry is the artifact at url, added to the record when it is not there yet.
func (p *published) entry(url string) *tracked {
	t := p.byURL(url)
	if t == nil {
		t = &tracked{URL: strings.TrimSuffix(url, "/")}
		p.Artifacts = append(p.Artifacts, t)
	}
	return t
}

// setFile notes that file holds t's page at version, and no other artifact's.
func (p *published) setFile(t *tracked, file, version string) {
	file = canonical(file)
	for _, o := range p.Artifacts {
		if o != t {
			delete(o.Files, file)
		}
	}
	if t.Files == nil {
		t.Files = map[string]string{}
	}
	t.Files[file] = version
}

// saw notes that this machine published or read version of t; Version keeps the newest.
func (t *tracked) saw(version string) {
	if version == "" {
		return
	}
	if t.Version == "" || versionSecs(version) >= versionSecs(t.Version) {
		t.Version = version
	}
}

// recordPublish notes that file was published as res; file is empty for a page fetched only to be edited.
func (p *published) recordPublish(file string, res *headless.Result, session, organizationID string, now time.Time) {
	t := p.entry(res.URL)
	if file != "" {
		p.setFile(t, file, res.Version)
	}
	t.saw(res.Version)
	if res.Title != "" {
		t.Title = res.Title
	}
	if session != "" && !slices.Contains(t.Sessions, session) {
		t.Sessions = append(t.Sessions, session)
	}
	if organizationID != "" {
		t.OrganizationID = organizationID
	}
	t.PublishedAt = now.UTC().Format(time.RFC3339)
}

// recordRead notes that file holds url's page at version, so the file may be published over that version.
func (p *published) recordRead(file, url, version string) {
	t := p.entry(url)
	p.setFile(t, file, version)
	t.saw(version)
}

// versionSecs is the Unix second a version id starts with, or -1 when it starts with none.
func versionSecs(version string) int64 {
	secs, err := strconv.ParseInt(strings.SplitN(version, "-", 2)[0], 10, 64)
	if err != nil {
		return -1
	}
	return secs
}

// newerThan reports whether a version published at updatedAt is newer than version, whose id starts with the
// Unix second it was published. It is false when either is unknown.
func newerThan(updatedAt, version string) bool {
	at, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return false
	}
	secs := versionSecs(version)
	return secs >= 0 && at.Unix() > secs
}
