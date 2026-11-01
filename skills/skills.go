// Package skills embeds the discovery stubs a host installs, and the digests of every stub ever shipped, so
// the installer can tell an untouched copy of ours from one somebody edited.
package skills

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"regexp"
	"strings"

	skillguides "github.com/tyrantkhan/claude-artifact-helper/skill-guides"
)

// Skill is one installable skill: the stub a host reads, and the guide the binary serves.
type Skill struct {
	Name        string
	Description string
	Stub        []byte
	Guide       []byte
	// Released holds the digest of every stub version ever shipped, the current one included.
	Released []string
}

//go:embed claude-artifact-helper/SKILL.md
var claudeArtifactHelperStub []byte

//go:embed released-digests.txt
var releasedDigests []byte

// All lists the skills this build ships, by name.
func All() []Skill {
	return []Skill{{
		Name:        "claude-artifact-helper",
		Description: "Publish, edit, share and answer comments on claude.ai artifacts through the human's Claude subscription.",
		Stub:        claudeArtifactHelperStub,
		Guide:       skillguides.ClaudeArtifactHelper,
		Released:    released("claude-artifact-helper"),
	}}
}

// Find returns the skill called name.
func Find(name string) (Skill, bool) {
	for _, s := range All() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// Digest is the sha256, in hex, of content with CRLF line endings folded to LF, so a copy saved on Windows
// still matches.
func Digest(content []byte) string {
	sum := sha256.Sum256(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func released(name string) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(releasedDigests))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == name && !strings.HasPrefix(f[0], "#") {
			out = append(out, f[0])
		}
	}
	return out
}

var nameLine = regexp.MustCompile(`(?m)^name:\s*(\S+)\s*$`)

// StubName reads the name from a SKILL.md's frontmatter, or "" when it has none.
func StubName(content []byte) string {
	head := content
	if len(head) > 4096 {
		head = head[:4096]
	}
	if !bytes.HasPrefix(bytes.TrimLeft(head, "\xef\xbb\xbf"), []byte("---")) {
		return ""
	}
	if m := nameLine.FindSubmatch(head); m != nil {
		return string(m[1])
	}
	return ""
}
