package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tyrantkhan/artifact-helper/internal/fakeclaude"
)

const (
	firstLink  = "https://claude.ai/artifact/First1"
	secondLink = "https://claude.ai/artifact/Second2"
)

// published answers a publish run as the Artifact tool does: its own data names the link and the version.
func publishedAs(url, version string, updated bool) string {
	return fakeclaude.Calls(`{"type":"result","subtype":"success","structured_output":{"url":"","error":""}}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"publish"}`, Text: "Published",
			Data: fmt.Sprintf(`{"url":%q,"updated":%v,"title":"Q3","version":%q}`, url, updated, version)})
}

// tracking is a machine set up and signed in, whose fake claude can be swapped while its config and record stay.
func tracking(t *testing.T) (*machine, string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "record")
	m := newMachine(t, fakeclaude.Spec{Record: record})
	if err := saveConfig(m.config, config{ClaudeConfigDir: "default"}); err != nil {
		t.Fatal(err)
	}
	m.getenv[sessionVar] = "session-1"
	return m, record
}

// answer makes the machine's claude answer each task with the given stdout.
func (m *machine) answer(t *testing.T, record string, outputs map[string]string) {
	t.Helper()
	m.bin = fakeclaude.Install(t, fakeclaude.Spec{Version: "9.9.9 (Claude Code)", Auth: exampleLogin, Record: record, Outputs: outputs,
		Files: map[string]string{"index.html": "<p>live</p>"}})
}

func (m *machine) record(t *testing.T) *published {
	t.Helper()
	p, err := loadRecord(&Env{Config: m.config})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func lastPrompt(t *testing.T, record string) string {
	t.Helper()
	runs := fakeclaude.Runs(t, record)
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i][1] == "-p" {
			return runs[i][len(runs[i])-1]
		}
	}
	t.Fatal("no run")
	return ""
}

func page(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>Q3</title><p>Draft</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestThePublishedFileKeepsItsLink(t *testing.T) {
	m, record := tracking(t)
	p := page(t)
	// First: a new artifact, recorded against the file and the calling session.
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, "1793629160-0a57", false)})
	if code, out, _ := m.run("", "publish", p); code != 0 || result(t, out)["url"] != firstLink {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byFile(p); e == nil || e.URL != firstLink || e.Version != "1793629160-0a57" || e.Title != "Q3" || e.Sessions[0] != "session-1" {
		t.Fatalf("record = %+v", e)
	}
	// Again with no --url: the same artifact, only over the version it recorded.
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, "1793700000-c0de", true)})
	if code, out, _ := m.run("", "publish", p); code != 0 || result(t, out)["updated"] != true {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, `"url":"`+firstLink+`"`) || !strings.Contains(prompt, `"1793629160-0a57"`) {
		t.Fatalf("prompt = %s", prompt)
	}
	if e := m.record(t).byFile(p); e.Version != "1793700000-c0de" {
		t.Fatalf("record = %+v", e)
	}
	// --new: a new artifact for the file, and the old link now forwards to it.
	m.answer(t, record, map[string]string{"publish": publishedAs(secondLink, "1793700001-d00d", false)})
	if code, out, _ := m.run("", "publish", p, "--new"); code != 0 || result(t, out)["url"] != secondLink {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if rec := m.record(t); len(rec.Artifacts) != 1 || rec.Artifacts[0].URL != secondLink || rec.Artifacts[0].Aliases[0] != firstLink {
		t.Fatalf("record = %+v", rec.Artifacts[0])
	}
	m.answer(t, record, map[string]string{"publish": publishedAs(secondLink, "1793700002-e00e", true)})
	code, out, _ := m.run("", "publish", p, "--url", firstLink)
	if code != 0 || result(t, out)["forwardedFrom"] != firstLink || !strings.Contains(lastPrompt(t, record), `"url":"`+secondLink+`"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if code, out, _ := m.run("", "publish", p, "--new", "--url", firstLink); code != 1 || refusalCode(t, out) != "invalid-args" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestEditAndReadFollowTheRecord(t *testing.T) {
	m, record := tracking(t)
	p := page(t)
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, "1793629160-0a57", false)})
	if code, out, _ := m.run("", "publish", p); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// edit takes the published file and finds its link.
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, "1793700000-c0de", true)})
	code, out, _ := m.run(`[{"old_str":"Draft","new_str":"Final"}]`, "edit", p)
	if code != 0 || result(t, out)["url"] != firstLink || !strings.Contains(lastPrompt(t, record), `"1793629160-0a57"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byFile(p); e.Version != "1793700000-c0de" {
		t.Fatalf("record = %+v", e)
	}
	if code, out, _ := m.run(`[]`, "edit", filepath.Join(t.TempDir(), "never.html")); code != 1 || refusalCode(t, out) != "invalid-args" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A read records the version it fetched, so a page merged from it may be published over it.
	read := fakeclaude.Calls(`{"type":"result","subtype":"success","structured_output":{"path":"index.html","error":""}}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"read"}`, Data: `{"file_read":{"ver":"1793800000-f00d"}}`, Text: "File saved"})
	m.answer(t, record, map[string]string{"read": read})
	if code, out, _ := m.run("", "read", firstLink, "--out", t.TempDir()); code != 0 || result(t, out)["version"] != "1793800000-f00d" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byURL(firstLink); e.Version != "1793800000-f00d" {
		t.Fatalf("record = %+v", e)
	}
}

func TestUpdatesReportsThisSessionsArtifacts(t *testing.T) {
	m, record := tracking(t)
	if err := changeRecord(&Env{Config: m.config}, func(p *published) {
		p.Artifacts = append(p.Artifacts,
			&tracked{URL: firstLink, Title: "Q3", File: "/p.html", Sessions: []string{"session-1"}, Version: "1793629160-0a57"},
			&tracked{URL: secondLink, Title: "Other", Sessions: []string{"session-2"}, Version: "1793629160-0a57"})
	}); err != nil {
		t.Fatal(err)
	}
	stream := fakeclaude.Calls(`{"type":"result","subtype":"success","result":"done"}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"list"}`, Text: "listed",
			Data: `{"artifacts":[{"url":"` + firstLink + `","updatedAt":"2026-11-02T20:00:00Z"}]}`},
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"comments","url":"` + firstLink + `"}`, Text: "2 threads",
			Data: `{"names":{"a1":"Sam"},"threads":[` +
				`{"id":"t1","resolved":false,"claude_activated":true,"comments":[{"id":"c1","account":"a1","text":"Fix the typo","awaiting_reply":true}]},` +
				`{"id":"t2","resolved":true,"claude_activated":false,"comments":[{"id":"c2","account":"a1","text":"Done"}]}]}`})
	m.answer(t, record, map[string]string{"updates": stream})
	code, out, _ := m.run("", "updates")
	r := result(t, out)
	arts, _ := r["artifacts"].([]any)
	if code != 0 || r["session"] != "session-1" || len(arts) != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	a := arts[0].(map[string]any)
	if a["url"] != firstLink || a["changedElsewhere"] != true || a["openThreads"] != 1.0 || len(a["waiting"].([]any)) != 1 || len(a["newComments"].([]any)) != 2 {
		t.Fatalf("artifact = %v", a)
	}
	// What was new is seen now; the next check reports nothing new.
	code, out, _ = m.run("", "updates")
	if a := result(t, out)["artifacts"].([]any)[0].(map[string]any); code != 0 || len(a["newComments"].([]any)) != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// --all covers every artifact this machine published.
	if !strings.Contains(lastPrompt(t, record), firstLink) {
		t.Fatal("no prompt")
	}
	m.run("", "updates", "--all")
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, firstLink) || !strings.Contains(prompt, secondLink) {
		t.Fatalf("prompt = %s", prompt)
	}
}
