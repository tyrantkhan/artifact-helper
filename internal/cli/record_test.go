package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tyrantkhan/artifact-helper/internal/fakeclaude"
)

const (
	firstLink  = "https://claude.ai/artifact/First1"
	secondLink = "https://claude.ai/artifact/Second2"
	thirdLink  = "https://claude.ai/artifact/Third3"
	readLink   = "https://claude.ai/artifact/ReadOnly4"

	v1 = "1793629160-0a57" // 2026-11-02T14:19:20Z
	v2 = "1793700000-c0de"
	v3 = "1793800000-f00d"
)

// publishedAs answers a publish run as the Artifact tool does: its own data names the link and the version.
func publishedAs(url, version string, updated bool) string {
	return fakeclaude.Calls(`{"type":"result","subtype":"success","structured_output":{"url":"","error":""}}`, publishCall(url, version, updated))
}

func publishCall(url, version string, updated bool) fakeclaude.Call {
	return fakeclaude.Call{Tool: "Artifact", Input: `{"action":"publish"}`, Text: "Published",
		Data: fmt.Sprintf(`{"url":%q,"updated":%v,"title":"Q3","version":%q}`, url, updated, version)}
}

// readAs answers a read run that saved the page at version.
func readAs(version string) string {
	return fakeclaude.Calls(`{"type":"result","subtype":"success","structured_output":{"path":"index.html","error":""}}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"read"}`, Data: `{"file_read":{"ver":"` + version + `"}}`, Text: "File saved"})
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

// prompts are the prompts of the headless runs so far.
func prompts(t *testing.T, record string) []string {
	t.Helper()
	var out []string
	for _, r := range fakeclaude.Runs(t, record) {
		if r[1] == "-p" {
			out = append(out, r[len(r)-1])
		}
	}
	return out
}

func lastPrompt(t *testing.T, record string) string {
	t.Helper()
	ps := prompts(t, record)
	if len(ps) == 0 {
		t.Fatal("no run")
	}
	return ps[len(ps)-1]
}

func page(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>Q3</title><p>Draft</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// publish publishes file, answered as a publish that made version at url, and fails the test unless it worked.
func (m *machine) publish(t *testing.T, record, url, version string, args ...string) map[string]any {
	t.Helper()
	m.answer(t, record, map[string]string{"publish": publishedAs(url, version, version != v1)})
	code, out, _ := m.run("", append([]string{"publish"}, args...)...)
	if code != 0 || result(t, out)["url"] != url {
		t.Fatalf("publish %v: code=%d out=%s", args, code, out)
	}
	return result(t, out)
}

func TestThePublishedFileKeepsItsLink(t *testing.T) {
	m, record := tracking(t)
	p := page(t)
	// First: a new artifact, recorded against the file and the calling session.
	m.publish(t, record, firstLink, v1, p)
	if e := m.record(t).byFile(p); e == nil || e.URL != firstLink || e.base(p) != v1 || e.Title != "Q3" || e.Sessions[0] != "session-1" || e.PublishedAt == "" {
		t.Fatalf("record = %+v", e)
	}
	// Again with no --url: the same artifact, only over the version the file was published as.
	if r := m.publish(t, record, firstLink, v2, p); r["updated"] != true {
		t.Fatalf("result = %v", r)
	}
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, `"url":"`+firstLink+`"`) || !strings.Contains(prompt, `"`+v1+`"`) {
		t.Fatalf("prompt = %s", prompt)
	}
	if e := m.record(t).byFile(p); e.base(p) != v2 || e.Version != v2 {
		t.Fatalf("record = %+v", e)
	}
	// --new: a new artifact for the file. The old one stays tracked, with its sessions, and says where the page went.
	m.publish(t, record, secondLink, v3, p, "--new")
	rec := m.record(t)
	if old, cur := rec.byURL(firstLink), rec.byFile(p); old.MovedTo != secondLink || len(old.Files) != 0 || len(old.Sessions) != 1 || old.PublishedAt == "" || cur.URL != secondLink {
		t.Fatalf("old = %+v, current = %+v", old, cur)
	}
	// The old link given to publish goes where the page lives now, over the file's version there.
	r := m.publish(t, record, secondLink, "1793900000-abcd", p, "--url", firstLink)
	if prompt := lastPrompt(t, record); r["forwardedFrom"] != firstLink || !strings.Contains(prompt, `"url":"`+secondLink+`"`) || !strings.Contains(prompt, `"`+v3+`"`) {
		t.Fatalf("result = %v, prompt = %s", r, prompt)
	}
	// Any other file given the old link updates the old artifact, which is still live.
	r = m.publish(t, record, firstLink, "1793900001-abcd", page(t), "--url", firstLink+"/")
	if prompt := lastPrompt(t, record); r["forwardedFrom"] != nil || !strings.Contains(prompt, `"url":"`+firstLink+`/"`) || !strings.Contains(prompt, `"`+v2+`"`) {
		t.Fatalf("result = %v, prompt = %s", r, prompt)
	}
	if code, out, _ := m.run("", "publish", p, "--new", "--url", firstLink); code != 1 || refusalCode(t, out) != "invalid-args" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestAFileGoesOnlyOverItsOwnVersion(t *testing.T) {
	m, record := tracking(t)
	p := page(t)
	m.publish(t, record, firstLink, v1, p)
	// Someone published v3. A read saves it, and the saved copy is the file that may go over it.
	m.answer(t, record, map[string]string{"read": readAs(v3)})
	code, out, _ := m.run("", "read", firstLink, "--out", t.TempDir())
	if code != 0 || result(t, out)["version"] != v3 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	saved := result(t, out)["savedPath"].(string)
	if e := m.record(t).byFile(saved); e == nil || e.URL != firstLink || e.base(saved) != v3 || e.base(p) != v1 || e.Version != v3 {
		t.Fatalf("record = %+v", e)
	}
	// The page published before still goes over v1 only, so its stale content cannot replace v3.
	m.publish(t, record, firstLink, v2, p)
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, `"`+v1+`"`) || strings.Contains(prompt, v3) {
		t.Fatalf("prompt = %s", prompt)
	}
	// The saved copy, with the changes merged in, goes to the same artifact over v3, with no --url.
	m.publish(t, record, firstLink, "1793900000-abcd", saved)
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, `"url":"`+firstLink+`"`) || !strings.Contains(prompt, `"`+v3+`"`) {
		t.Fatalf("prompt = %s", prompt)
	}
	// Another of the artifact's files holds no page, so reading it records nothing.
	before := m.record(t).byURL(firstLink)
	m.answer(t, record, map[string]string{"read": readAs("1794000000-0000")})
	if code, out, _ := m.run("", "read", firstLink, "--path", "assets/app.js", "--out", t.TempDir()); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byURL(firstLink); e.Version != before.Version || len(e.Files) != len(before.Files) {
		t.Fatalf("record = %+v, before %+v", e, before)
	}
}

func TestALinkThisMachineReadIsGuarded(t *testing.T) {
	m, record := tracking(t)
	m.answer(t, record, map[string]string{"read": readAs(v3)})
	if code, out, _ := m.run("", "read", readLink+"/", "--out", t.TempDir()); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// Read, never published: tracked for its version only.
	// Stored without the slash, the link matches the account listing that updates reads.
	if e := m.record(t).byURL(readLink); e == nil || e.URL != readLink || e.Version != v3 || len(e.Sessions) != 0 || e.PublishedAt != "" {
		t.Fatalf("record = %+v", e)
	}
	// A page written to replace it goes only over the version read.
	m.publish(t, record, readLink, "1793900000-abcd", page(t), "--url", readLink)
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, `"`+v3+`"`) {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestFilesAreKnownByTheirRealPath(t *testing.T) {
	m, record := tracking(t)
	dir := t.TempDir()
	via := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(dir, via); err != nil {
		t.Skip("this system cannot make a symlink: ", err)
	}
	p := filepath.Join(dir, "page.html")
	if err := os.WriteFile(p, []byte("<p>Draft</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.publish(t, record, firstLink, v1, p)
	m.publish(t, record, firstLink, v2, filepath.Join(via, "page.html"))
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, `"url":"`+firstLink+`"`) || !strings.Contains(prompt, `"`+v1+`"`) {
		t.Fatalf("prompt = %s", prompt)
	}
	// The page named both ways at once is still one page.
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, v3, true)})
	if code, out, _ := m.run(`[{"old_str":"Draft","new_str":"Final"}]`, "edit", filepath.Join(via, "page.html"), "--file", p); code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestEditKeepsTheFileInStep(t *testing.T) {
	m, record := tracking(t)
	p := page(t)
	m.publish(t, record, firstLink, v1, p)
	// edit takes the published file, finds its link and goes over the file's version.
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, v2, true)})
	code, out, _ := m.run(`[{"old_str":"Draft","new_str":"Final"}]`, "edit", p)
	if code != 0 || result(t, out)["url"] != firstLink || !strings.Contains(lastPrompt(t, record), `"`+v1+`"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byFile(p); e.base(p) != v2 {
		t.Fatalf("record = %+v", e)
	}
	if code, out, _ := m.run(`[]`, "edit", filepath.Join(t.TempDir(), "never.html")); code != 1 || refusalCode(t, out) != "invalid-args" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if code, out, _ := m.run(`[]`, "edit", p, "--file", page(t)); code != 1 || refusalCode(t, out) != "invalid-args" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// Given the link and the file, edit goes over the file's version.
	m.answer(t, record, map[string]string{"publish": publishedAs(firstLink, "1793750000-1111", true)})
	code, out, _ = m.run(`[{"old_str":"Final","new_str":"Done"}]`, "edit", firstLink, "--file", p)
	if code != 0 || !strings.Contains(lastPrompt(t, record), `"`+v2+`"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// Given the link, edit fetches the live page and goes over the version it fetched. The fetched copy is
	// temporary, so it is not recorded, and the published file keeps its version: it lacks this edit.
	m.answer(t, record, map[string]string{"read": readAs(v3), "publish": publishedAs(firstLink, "1793900000-abcd", true)})
	code, out, _ = m.run(`[{"old_str":"live","new_str":"LIVE"}]`, "edit", firstLink)
	if code != 0 || result(t, out)["fetched"] != true || !strings.Contains(lastPrompt(t, record), `"`+v3+`"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byURL(firstLink); len(e.Files) != 1 || e.base(p) != "1793750000-1111" || e.Version != "1793900000-abcd" {
		t.Fatalf("record = %+v", e)
	}
}

func TestAPublishThatWentThroughIsReported(t *testing.T) {
	m, record := tracking(t)
	p := page(t)
	m.publish(t, record, firstLink, v1, p)
	// The run should have stopped at the version it read, but published over it: the result says so.
	over := fakeclaude.Calls(`{"type":"result","subtype":"success","structured_output":{"url":"","error":""}}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"read"}`, Data: `{"artifactRead":{"ver":"1793650000-aaaa"}}`, Text: "read"},
		publishCall(firstLink, v2, true))
	m.answer(t, record, map[string]string{"publish": over})
	code, out, _ := m.run("", "publish", p)
	if code != 0 || result(t, out)["overwrote"] != "1793650000-aaaa" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A run that failed after its publish went through still reports the publish, and the record keeps it.
	failed := fakeclaude.Calls(`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"result":"over budget"}`, publishCall(firstLink, v3, true))
	m.answer(t, record, map[string]string{"publish": failed})
	code, out, _ = m.run("", "publish", p)
	if w, _ := result(t, out)["warning"].(string); code != 0 || !strings.Contains(w, "over budget") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if e := m.record(t).byFile(p); e.base(p) != v3 {
		t.Fatalf("record = %+v", e)
	}
	// A run stopped before its result line is read the same way.
	m.answer(t, record, map[string]string{"publish": fakeclaude.Calls("", publishCall(firstLink, "1793900000-abcd", true))})
	if code, out, _ := m.run("", "publish", p); code != 0 || result(t, out)["warning"] == nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// A failed run whose publish the tool refused reports the refusal, not the failure.
	refusedThenFailed := fakeclaude.Calls(`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"result":"over budget"}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"publish"}`, Data: "null", Text: "no such artifact", IsError: true})
	m.answer(t, record, map[string]string{"publish": refusedThenFailed})
	if code, out, _ := m.run("", "publish", p); code != 1 || refusalCode(t, out) != "publish-refused" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestTheNewestVersionSeenIsKept(t *testing.T) {
	var e tracked
	for _, v := range []string{v2, v1, "", v3, v2} {
		e.saw(v)
	}
	if e.Version != v3 {
		t.Fatalf("version = %q", e.Version)
	}
}

func TestTheRecordIsSavedWholeOrRefused(t *testing.T) {
	m, record := tracking(t)
	m.publish(t, record, firstLink, v1, page(t))
	dir := filepath.Dir(m.config)
	if left, _ := filepath.Glob(filepath.Join(dir, "published-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	if err := os.WriteFile(filepath.Join(dir, "published.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := m.run("", "publish", page(t)); code != 1 || refusalCode(t, out) != "invalid-record" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestARecordThatCannotBeSavedDoesNotHideThePublish(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not stop writes into a read-only folder")
	}
	m, record := tracking(t)
	m.publish(t, record, firstLink, v1, page(t))
	dir := filepath.Dir(m.config)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	r := m.publish(t, record, secondLink, v1, page(t))
	if e, _ := r["recordError"].(string); !strings.Contains(e, "published.json") {
		t.Fatalf("result = %v", r)
	}
}

func TestUpdatesReportsThisSessionsArtifacts(t *testing.T) {
	m, record := tracking(t)
	if err := changeRecord(&Env{Config: m.config}, func(p *published) {
		at := "2026-11-01T00:00:00Z"
		p.Artifacts = append(p.Artifacts,
			&tracked{URL: firstLink, Title: "Q3", Files: map[string]string{"/p.html": v1}, Sessions: []string{"session-1"}, Version: v1, PublishedAt: at},
			&tracked{URL: secondLink, Title: "Other", Sessions: []string{"session-2"}, Version: v1, PublishedAt: at},
			&tracked{URL: thirdLink, Sessions: []string{"session-1"}, Version: v1, PublishedAt: at, Seen: []string{"old"}},
			&tracked{URL: readLink, Version: v1})
	}); err != nil {
		t.Fatal(err)
	}
	comment := func(id, text, extra string) string {
		return `{"id":"` + id + `","account":"a1","text":"` + text + `"` + extra + `}`
	}
	stream := fakeclaude.Calls(`{"type":"result","subtype":"success","result":"done"}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"list"}`, Text: "listed",
			Data: `{"artifacts":[{"url":"` + firstLink + `","updatedAt":"2026-11-03T00:00:00Z"},{"url":"` + thirdLink + `","updatedAt":"2026-11-02T14:19:20Z"}]}`},
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"comments","url":"` + firstLink + `"}`, Text: "4 threads",
			Data: `{"names":{"a1":"Sam"},"threads":[` +
				`{"id":"t1","resolved":false,"claude_activated":true,"comments":[` + comment("c1", "Fix the typo", `,"awaiting_reply":true`) + `]},` +
				`{"id":"t2","resolved":true,"claude_activated":false,"comments":[` + comment("c2", "Done", "") + `]},` +
				`{"id":"t3","resolved":false,"claude_activated":true,"comments":[` + comment("c3", "Answered", "") + `]},` +
				`{"id":"t4","resolved":false,"claude_activated":false,"comments":[` + comment("c4", "Not sent", `,"awaiting_reply":true`) + `]}]}`},
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"comments","url":"` + thirdLink + `"}`, Data: "null", Text: "not allowed", IsError: true})
	m.answer(t, record, map[string]string{"updates": stream})
	code, out, _ := m.run("", "updates")
	r := result(t, out)
	arts, _ := r["artifacts"].([]any)
	if code != 0 || r["session"] != "session-1" || len(arts) != 2 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// Only a thread sent to Claude and waiting on it waits; one Claude answered, or nobody sent, does not.
	a := arts[0].(map[string]any)
	if a["url"] != firstLink || a["changedElsewhere"] != true || a["openThreads"] != 3.0 || fmt.Sprint(a["waiting"]) != "[t1]" || len(a["newComments"].([]any)) != 4 {
		t.Fatalf("artifact = %v", a)
	}
	// Published at the very second of the version this machine has: not changed elsewhere. Its comments could not be
	// read, so what was seen before stays seen.
	if c := arts[1].(map[string]any); c["url"] != thirdLink || c["changedElsewhere"] != false || c["error"] == nil {
		t.Fatalf("artifact = %v", c)
	}
	if e := m.record(t).byURL(thirdLink); fmt.Sprint(e.Seen) != "[old]" || e.CheckedAt != "" {
		t.Fatalf("record = %+v", e)
	}
	// What was new is seen now; the next check reports nothing new.
	code, out, _ = m.run("", "updates")
	if a := result(t, out)["artifacts"].([]any)[0].(map[string]any); code != 0 || len(a["newComments"].([]any)) != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	// --all covers every artifact this machine published, and none it only read.
	m.run("", "updates", "--all")
	if prompt := lastPrompt(t, record); !strings.Contains(prompt, secondLink) || strings.Contains(prompt, readLink) {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestUpdatesChecksFiftyAtATime(t *testing.T) {
	m, record := tracking(t)
	if err := changeRecord(&Env{Config: m.config}, func(p *published) {
		for i := range 51 {
			p.Artifacts = append(p.Artifacts, &tracked{URL: fmt.Sprintf("https://claude.ai/artifact/Batch%02d", i),
				Sessions: []string{"session-1"}, PublishedAt: "2026-11-01T00:00:00Z"})
		}
	}); err != nil {
		t.Fatal(err)
	}
	m.answer(t, record, map[string]string{"updates": fakeclaude.Calls(`{"type":"result","subtype":"success","result":"done"}`,
		fakeclaude.Call{Tool: "Artifact", Input: `{"action":"list"}`, Data: `{"artifacts":[]}`, Text: "listed"})})
	code, out, _ := m.run("", "updates")
	if arts, _ := result(t, out)["artifacts"].([]any); code != 0 || len(arts) != 51 || len(prompts(t, record)) != 2 {
		t.Fatalf("code=%d runs=%d out=%.300s", code, len(prompts(t, record)), out)
	}
	if ps := prompts(t, record); !strings.Contains(ps[1], "Batch50") || strings.Contains(ps[1], "Batch49") {
		t.Fatalf("second run = %s", ps[1])
	}
}
