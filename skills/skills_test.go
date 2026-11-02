package skills

import (
	"slices"
	"strings"
	"testing"
)

func TestEveryShippedStubDigestIsReleased(t *testing.T) {
	for _, s := range All() {
		d := Digest(s.Stub)
		if !slices.Contains(s.Released, d) {
			t.Errorf("%s: the stub's digest %s is not in skills/released-digests.txt; append it (the stub changed)", s.Name, d)
		}
		if StubName(s.Stub) != s.Name {
			t.Errorf("%s: the stub's frontmatter names %q", s.Name, StubName(s.Stub))
		}
		if len(s.Guide) == 0 || !strings.Contains(string(s.Stub), "get-skill") {
			t.Errorf("%s: the stub does not say how to load its guide", s.Name)
		}
	}
	if Digest([]byte("a\r\nb")) != Digest([]byte("a\nb")) {
		t.Error("CRLF and LF digests differ")
	}
	if StubName([]byte("# no frontmatter\nname: x\n")) != "" {
		t.Error("a name outside frontmatter was read")
	}
}

func TestFind(t *testing.T) {
	if s, ok := Find("artifact-helper"); !ok || s.Name != "artifact-helper" {
		t.Fatalf("Find(artifact-helper) = %+v, %v", s, ok)
	}
	if _, ok := Find("nope"); ok {
		t.Fatal("Find(nope) found a skill")
	}
}

func TestStubNameReadsOnlyTheFrontmatter(t *testing.T) {
	for in, want := range map[string]string{
		"---\nname: artifact-helper\n---\n# x\n":              "artifact-helper",
		"\xef\xbb\xbf---\r\nname: artifact-helper\r\n---\r\n": "artifact-helper",
		"---\ndescription: x\n---\nname: artifact-helper\n":   "",
		"name: artifact-helper\n":                             "",
		"---\nname: artifact-helper\n":                        "",
	} {
		if got := StubName([]byte(in)); got != want {
			t.Errorf("StubName(%q) = %q, want %q", in, got, want)
		}
	}
}
