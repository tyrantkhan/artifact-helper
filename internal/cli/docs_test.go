package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tyrantkhan/artifact-helper/skills"
)

// refusalCodes collects every refusal code the code can return, from the Go sources of the packages that make
// them: each Code: "..." literal, and what-refused for each refused("what", ...).
func refusalCodes(t *testing.T) []string {
	t.Helper()
	literal := regexp.MustCompile(`Code:\s*"([a-z-]+)"`)
	family := regexp.MustCompile(`refused\("([a-z]+)"`)
	var codes []string
	for _, dir := range []string{".", "../headless", "../skillinstall"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range literal.FindAllSubmatch(b, -1) {
				codes = append(codes, string(m[1]))
			}
			for _, m := range family.FindAllSubmatch(b, -1) {
				codes = append(codes, string(m[1])+"-refused")
			}
		}
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

// The guide and the README tell agents and people what each refusal means, so each code the code returns
// must be named in both.
func TestEveryRefusalCodeIsDocumented(t *testing.T) {
	codes := refusalCodes(t)
	if len(codes) < 20 {
		t.Fatalf("found only %v; the pattern no longer matches the code", codes)
	}
	guide := string(skills.All()[0].Guide)
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		// The guide covers the -refused family by its suffix; no-answer only reaches a person at a terminal.
		inGuide := strings.Contains(guide, "`"+code+"`") ||
			(strings.HasSuffix(code, "-refused") && strings.Contains(guide, "ending in `-refused`")) || code == "no-answer"
		if !inGuide {
			t.Errorf("the guide does not name %s", code)
		}
		if !strings.Contains(string(readme), "`"+code+"`") {
			t.Errorf("the README does not name %s", code)
		}
	}
}

// The hook builds its JSON with printf and no escaping, so its context text must hold nothing that would
// break the JSON or the shell quoting around it.
func TestTheHookContextNeedsNoEscaping(t *testing.T) {
	b, err := os.ReadFile("../../hooks/session-start")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^context='([^']*)'$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no context='...' line in hooks/session-start")
	}
	if i := strings.IndexAny(string(m[1]), "\"\\\n\t"); i >= 0 {
		t.Fatalf("the context holds %q, which printf would put into the JSON unescaped", m[1][i])
	}
}
