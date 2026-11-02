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
