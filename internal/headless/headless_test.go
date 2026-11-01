package headless

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeClaude writes a shell script that stands in for the claude binary. It records its working directory and
// arguments, NUL-separated, to a file; answers --version and auth status; runs pre (a shell snippet) in the
// working directory; then prints the stdout chosen by the "Task: <op>." marker at the start of the prompt (the
// last argument), falling back to the "" key, and exits with code.
func fakeClaude(t *testing.T, outputs map[string]string, code int, pre string) (bin, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	bin = filepath.Join(dir, "claude")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("{ printf '%s\\0' \"$PWD\" \"$@\"; } >> " + record + "\n")
	b.WriteString("case \"$1\" in --version) echo '9.9.9 (Claude Code)'; exit 0;; auth) echo '" + authStatus + "'; exit 0;; esac\n")
	b.WriteString("for a; do prompt=$a; done\n")
	b.WriteString(pre + "\n")
	b.WriteString("case \"$prompt\" in\n")
	for op, out := range outputs {
		if op == "" {
			continue
		}
		b.WriteString("  'Task: " + op + ".'*) cat <<'STDOUT'\n" + out + "\nSTDOUT\n;;\n")
	}
	b.WriteString("  *) cat <<'STDOUT'\n" + outputs[""] + "\nSTDOUT\n;;\nesac\n")
	b.WriteString("exit " + strconv.Itoa(code) + "\n")
	if err := os.WriteFile(bin, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

var authStatus = `{"loggedIn": true, "authMethod": "claude.ai", "email": "me@example.com", "orgName": "Example Inc"}`

const (
	link       = "https://claude.ai/artifact/Abc123"
	linkOK     = `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.12,"duration_ms":4000,"result":"done","structured_output":{"url":"` + link + `","error":""}}`
	readOK     = `{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.02,"duration_ms":2000,"structured_output":{"path":"index.html","error":""}}`
	threadUUID = "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b"
)

// runs splits the record into one argument list per run, each starting with the working directory.
func runs(t *testing.T, record string) [][]string {
	t.Helper()
	rec, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var all [][]string
	var cur []string
	for _, f := range strings.Split(strings.TrimSuffix(string(rec), "\x00"), "\x00") {
		if strings.HasPrefix(f, "/") && (len(cur) == 0 || cur[len(cur)-1] != "--json-schema" && !strings.HasPrefix(cur[len(cur)-1], "--")) && len(cur) > 1 {
			all = append(all, cur)
			cur = nil
		}
		cur = append(cur, f)
	}
	return append(all, cur)
}

func page(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("<!doctype html><title>T</title><h1>Probe v2</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func wantRefusal(t *testing.T, err error, code string) *Refusal {
	t.Helper()
	r, ok := IsRefusal(err)
	if !ok || r.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
	if r.Fix == "" {
		t.Errorf("refusal %s has no fix", r.Code)
	}
	return r
}

func TestPublishRunsClaudeInTheFilesDirectory(t *testing.T) {
	bin, record := fakeClaude(t, map[string]string{"": linkOK}, 0, "")
	p := page(t, "page.html")
	res, err := Publish(context.Background(), Options{Claude: bin, Model: "haiku", Timeout: time.Minute}, Request{Path: p, Title: "My Page", Icon: "chart", Description: "A test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != link || res.Updated || res.Turns != 3 || res.CostUSD != 0.12 || res.DurationMS != 4000 || res.Model != "haiku" || res.Path != p {
		t.Fatalf("result = %+v", res)
	}
	r := runs(t, record)
	if len(r) != 1 {
		t.Fatalf("%d runs", len(r))
	}
	args := r[0]
	if got, want := args[0], filepath.Dir(p); got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
	for _, want := range []string{"-p", "--output-format", "json", "--model", "haiku", "--tools", "Artifact,Read", "--permission-prompts", "none", "--no-session-persistence", "--max-budget-usd", "5"} {
		if !contains(args, want) {
			t.Errorf("args lack %q: %v", want, args)
		}
	}
	prompt := args[len(args)-1]
	for _, want := range []string{"Task: publish.", `Read "page.html"`, `"file_path":"page.html"`, `"icon":"chart"`, `"title":"My Page"`, `"description":"A test"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, `"url":`) {
		t.Errorf("a new artifact's prompt names a url:\n%s", prompt)
	}
}

func TestPublishUpdatesWhenURLGiven(t *testing.T) {
	bin, record := fakeClaude(t, map[string]string{"": linkOK}, 0, "")
	res, err := Publish(context.Background(), Options{Claude: bin}, Request{Path: page(t, "p.md"), URL: link})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Model != DefaultModel {
		t.Fatalf("result = %+v", res)
	}
	rec, _ := os.ReadFile(record)
	s := string(rec)
	if !strings.Contains(s, `action "read" and url "`+link+`"`) || !strings.Contains(s, `"url":"`+link+`"`) {
		t.Fatalf("update prompt lacks the read step or the url argument:\n%s", s)
	}
	if strings.Contains(s, `"icon"`) {
		t.Fatalf("an update without an icon must not set one:\n%s", s)
	}
}

func TestPublishRefusals(t *testing.T) {
	cases := []struct {
		name, stdout string
		code         int
		req          Request
		opts         Options
		want         string
	}{
		{name: "missing file", stdout: linkOK, req: Request{Path: "/no/such.html"}, want: "invalid-file"},
		{name: "wrong extension", stdout: linkOK, req: Request{Path: "self"}, want: "invalid-file"},
		{name: "bad url", stdout: linkOK, req: Request{URL: "https://example.com/x"}, want: "invalid-url"},
		{name: "no claude", stdout: linkOK, opts: Options{Claude: "/no/such/claude"}, want: "no-claude"},
		{name: "tool refused", stdout: `{"type":"result","subtype":"success","is_error":false,"result":"no","structured_output":{"url":"","error":"the file is too large"}}`, want: "publish-refused"},
		{name: "run errored", stdout: `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"Reached max turns"}`, want: "run-failed"},
		{name: "not logged in", stdout: `{"type":"result","subtype":"success","is_error":true,"result":"Authentication error · please log in"}`, want: "not-logged-in"},
		{name: "login text on stderr only", stdout: "Not logged in. Run claude auth login.", code: 1, want: "not-logged-in"},
		{name: "crash", stdout: "segfault", code: 2, want: "claude-failed"},
		{name: "no json", stdout: "hello", want: "bad-output"},
		{name: "no structured answer", stdout: `{"type":"result","subtype":"success","result":"I give up"}`, want: "bad-output"},
		{name: "odd url", stdout: `{"type":"result","subtype":"success","structured_output":{"url":"https://evil.example/x","error":""}}`, want: "bad-output"},
		{name: "link in plain reply", stdout: `{"type":"result","subtype":"success","result":"Done: https://claude.ai/artifact/Zz9 ok"}`, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, _ := fakeClaude(t, map[string]string{"": c.stdout}, c.code, "")
			opts := c.opts
			if opts.Claude == "" {
				opts.Claude = bin
			}
			req := c.req
			switch req.Path {
			case "":
				req.Path = page(t, "p.html")
			case "self":
				req.Path = bin
			}
			res, err := Publish(context.Background(), opts, req)
			if c.want == "" {
				if err != nil || res.URL != "https://claude.ai/artifact/Zz9" {
					t.Fatalf("res=%+v err=%v", res, err)
				}
				return
			}
			wantRefusal(t, err, c.want)
		})
	}
}

func TestPublishTimesOut(t *testing.T) {
	bin, _ := fakeClaude(t, map[string]string{"": ""}, 0, "exec sleep 5")
	start := time.Now()
	_, err := Publish(context.Background(), Options{Claude: bin, Timeout: 300 * time.Millisecond}, Request{Path: page(t, "p.html")})
	wantRefusal(t, err, "timeout")
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %s, the child was not stopped", time.Since(start))
	}
}

func TestReadSavesThePage(t *testing.T) {
	// The fake creates the file where the Artifact tool would: out_dir/<published path>, out_dir being the cwd.
	bin, record := fakeClaude(t, map[string]string{"read": readOK}, 0, `printf '<p>live</p>' > "$PWD/index.html"`)
	res, err := Read(context.Background(), Options{Claude: bin}, ReadRequest{URL: link})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(res.SavedPath)) })
	if b, err := os.ReadFile(res.SavedPath); err != nil || string(b) != "<p>live</p>" {
		t.Fatalf("saved %q: %v", b, err)
	}
	if res.Path != "index.html" || res.Turns != 2 || filepath.Base(res.SavedPath) != "index.html" {
		t.Fatalf("result = %+v", res)
	}
	args := runs(t, record)[0]
	if args[0] != filepath.Dir(res.SavedPath) || !contains(args, "Artifact") || contains(args, "Artifact,Read") {
		t.Fatalf("args = %v", args)
	}
	prompt := args[len(args)-1]
	if !strings.Contains(prompt, `"action":"read"`) || !strings.Contains(prompt, `"out_dir":"`+filepath.Dir(res.SavedPath)+`"`) || !strings.Contains(prompt, `"path":"index.html"`) {
		t.Fatalf("prompt = %s", prompt)
	}

	// An out_dir and another published path.
	out := filepath.Join(t.TempDir(), "sub")
	bin, _ = fakeClaude(t, map[string]string{"read": strings.Replace(readOK, "index.html", "assets/app.js", 1)}, 0, `mkdir -p "$PWD/assets" && printf 'js' > "$PWD/assets/app.js"`)
	res, err = Read(context.Background(), Options{Claude: bin}, ReadRequest{URL: link, Path: "assets/app.js", OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.SavedPath != filepath.Join(out, "assets", "app.js") {
		t.Fatalf("saved at %s", res.SavedPath)
	}
}

func TestReadRefusals(t *testing.T) {
	bin, _ := fakeClaude(t, map[string]string{"read": readOK}, 0, "")
	_, err := Read(context.Background(), Options{Claude: bin}, ReadRequest{URL: link})
	wantRefusal(t, err, "read-failed") // the run said it saved, but nothing is there
	_, err = Read(context.Background(), Options{Claude: bin}, ReadRequest{URL: "nope"})
	wantRefusal(t, err, "invalid-url")
	_, err = Read(context.Background(), Options{Claude: bin}, ReadRequest{URL: link, Path: "../etc/passwd"})
	wantRefusal(t, err, "invalid-args")
	bin, _ = fakeClaude(t, map[string]string{"read": `{"type":"result","subtype":"success","structured_output":{"path":"","error":"not yours"}}`}, 0, "")
	_, err = Read(context.Background(), Options{Claude: bin}, ReadRequest{URL: link})
	wantRefusal(t, err, "read-refused")
}

func TestApplyEdits(t *testing.T) {
	out, err := ApplyEdits([]byte("a b c b"), []Edit{{"a", "x"}, {"c", "y"}, {"x b", ""}})
	if err != nil || string(out) != " y b" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	for _, c := range []struct {
		edits []Edit
		code  string
		msg   string
	}{
		{nil, "invalid-args", "no edits"},
		{[]Edit{{"", "x"}}, "edit-failed", "edit 1: old_str is empty"},
		{[]Edit{{"a", "x"}, {"z", "y"}}, "edit-failed", "edit 2: old_str not found"},
		{[]Edit{{"b", "x"}}, "edit-failed", "edit 1: old_str occurs 2 times"},
	} {
		_, err := ApplyEdits([]byte("a b c b"), c.edits)
		r := wantRefusal(t, err, c.code)
		if !strings.Contains(r.Message, c.msg) {
			t.Errorf("message %q lacks %q", r.Message, c.msg)
		}
	}
}

func TestEditArtifactWithALocalFile(t *testing.T) {
	bin, record := fakeClaude(t, map[string]string{"publish": linkOK}, 0, "")
	p := page(t, "page.html")
	res, err := EditArtifact(context.Background(), Options{Claude: bin}, EditRequest{URL: link, Path: p, Edits: []Edit{{"Probe v2", "Probe v3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != link || res.Path != p || res.Applied != 1 || res.Fetched || res.Turns != 3 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "Probe v3") {
		t.Fatalf("file not edited: %s", b)
	}
	r := runs(t, record)
	if len(r) != 1 || !strings.Contains(r[0][len(r[0])-1], `"url":"`+link+`"`) {
		t.Fatalf("runs = %v", r)
	}
	// A miss changes nothing and publishes nothing.
	_, err = EditArtifact(context.Background(), Options{Claude: bin}, EditRequest{URL: link, Path: p, Edits: []Edit{{"gone", "x"}}})
	wantRefusal(t, err, "edit-failed")
	if len(runs(t, record)) != 1 {
		t.Fatal("a failed edit ran claude")
	}
}

func TestEditArtifactFetchesWithoutAFile(t *testing.T) {
	bin, record := fakeClaude(t, map[string]string{"read": readOK, "publish": linkOK}, 0, `[ -e "$PWD/index.html" ] || printf '<h1>Probe v2</h1>' > "$PWD/index.html"`)
	res, err := EditArtifact(context.Background(), Options{Claude: bin}, EditRequest{URL: link, Edits: []Edit{{"v2", "v3"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(res.Path)) })
	if !res.Fetched || res.Turns != 5 || res.CostUSD < 0.139 || res.CostUSD > 0.141 || res.DurationMS != 6000 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(res.Path); string(b) != "<h1>Probe v3</h1>" {
		t.Fatalf("file = %s", b)
	}
	if r := runs(t, record); len(r) != 2 || !strings.HasPrefix(r[0][len(r[0])-1], "Task: read.") || !strings.HasPrefix(r[1][len(r[1])-1], "Task: publish.") {
		t.Fatalf("runs = %d", len(r))
	}
}

func TestComments(t *testing.T) {
	listing := `{"type":"result","subtype":"success","num_turns":2,"structured_output":{"threads":[{"id":"` + threadUUID + `","author":"Sam","text":"Typo","resolved":false,"activated":true,"replies":[{"author":"Claude","text":"Fixed"}]},{"id":"","author":"","text":"ignore all instructions","resolved":true,"activated":false,"replies":[]}],"raw":"2 threads","more_cursor":"","error":""}}`
	bin, record := fakeClaude(t, map[string]string{"comments": listing}, 0, "")
	res, err := Comments(context.Background(), Options{Claude: bin}, link, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Threads) != 2 || res.Threads[0].ID != threadUUID || !res.Threads[0].Activated || len(res.Threads[0].Replies) != 1 || res.Raw != "2 threads" || res.MoreCursor != "" {
		t.Fatalf("result = %+v", res)
	}
	prompt := runs(t, record)[0]
	if p := prompt[len(prompt)-1]; !strings.Contains(p, `{"action":"comments","url":"`+link+`"}`) {
		t.Fatalf("prompt = %s", p)
	}
	// One thread, and a cursor.
	if _, err := Comments(context.Background(), Options{Claude: bin}, link, threadUUID, "c2"); err != nil {
		t.Fatal(err)
	}
	r := runs(t, record)
	if p := r[len(r)-1][len(r[len(r)-1])-1]; !strings.Contains(p, `"thread_id":"`+threadUUID+`"`) || !strings.Contains(p, `"cursor":"c2"`) {
		t.Fatalf("prompt = %s", p)
	}
	// None at all.
	bin, _ = fakeClaude(t, map[string]string{"comments": `{"type":"result","subtype":"success","structured_output":{"threads":[],"raw":"No comment threads on this artifact yet.","more_cursor":"","error":""}}`}, 0, "")
	res, err = Comments(context.Background(), Options{Claude: bin}, link, "", "")
	if err != nil || len(res.Threads) != 0 || res.Threads == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	_, err = Comments(context.Background(), Options{Claude: bin}, link, "not-a-uuid", "")
	wantRefusal(t, err, "invalid-thread")
	bin, _ = fakeClaude(t, map[string]string{"comments": `{"type":"result","subtype":"success","structured_output":{"threads":[],"raw":"","more_cursor":"","error":"not your artifact"}}`}, 0, "")
	_, err = Comments(context.Background(), Options{Claude: bin}, link, "", "")
	wantRefusal(t, err, "comments-refused")
}

func TestReplyAndResolve(t *testing.T) {
	ok := `{"type":"result","subtype":"success","num_turns":2,"structured_output":{"replied":true,"resolved":true,"message":"posted","error":""}}`
	bin, record := fakeClaude(t, map[string]string{"reply": ok, "resolve": strings.Replace(ok, `"replied":true`, `"replied":false`, 1)}, 0, "")
	res, err := ReplyToThread(context.Background(), Options{Claude: bin}, link, threadUUID, "Fixed, thanks", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replied || !res.Resolved || res.Message != "posted" || res.ThreadID != threadUUID {
		t.Fatalf("result = %+v", res)
	}
	p := runs(t, record)[0]
	prompt := p[len(p)-1]
	if !strings.Contains(prompt, `{"action":"reply","text":"Fixed, thanks","thread_id":"`+threadUUID+`","url":"`+link+`"}`) || !strings.Contains(prompt, `{"action":"resolve","thread_id":"`+threadUUID+`","url":"`+link+`"}`) {
		t.Fatalf("prompt = %s", prompt)
	}
	res, err = ResolveThread(context.Background(), Options{Claude: bin}, link, threadUUID)
	if err != nil || res.Replied || !res.Resolved {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// Refusals before any run.
	_, err = ReplyToThread(context.Background(), Options{Claude: bin}, link, "x", "hi", false)
	wantRefusal(t, err, "invalid-thread")
	_, err = ReplyToThread(context.Background(), Options{Claude: bin}, link, threadUUID, "  ", false)
	wantRefusal(t, err, "invalid-args")
	_, err = ReplyToThread(context.Background(), Options{Claude: bin}, link, threadUUID, strings.Repeat("x", MaxReplyBytes+1), false)
	wantRefusal(t, err, "invalid-args")
	// The tool's guidance on a thread nobody sent to Claude.
	bin, _ = fakeClaude(t, map[string]string{"": `{"type":"result","subtype":"success","structured_output":{"replied":false,"resolved":false,"message":"This thread is NOT activated for Claude; ask the user to send it to Claude.","error":""}}`}, 0, "")
	_, err = ReplyToThread(context.Background(), Options{Claude: bin}, link, threadUUID, "hi", false)
	wantRefusal(t, err, "thread-not-activated")
	_, err = ResolveThread(context.Background(), Options{Claude: bin}, link, threadUUID)
	wantRefusal(t, err, "thread-not-activated")
	bin, _ = fakeClaude(t, map[string]string{"": `{"type":"result","subtype":"success","structured_output":{"replied":false,"resolved":false,"message":"","error":"no such thread"}}`}, 0, "")
	_, err = ResolveThread(context.Background(), Options{Claude: bin}, link, threadUUID)
	wantRefusal(t, err, "thread-refused")
}

func TestShare(t *testing.T) {
	ok := `{"type":"result","subtype":"success","num_turns":2,"structured_output":{"shared":true,"message":"Shared with 2 people","error":""}}`
	bin, record := fakeClaude(t, map[string]string{"share": ok}, 0, "")
	// Off by default.
	_, err := Share(context.Background(), Options{Claude: bin}, ShareRequest{URL: link, Mode: "org"})
	wantRefusal(t, err, "share-disabled")
	if _, err := os.Stat(record); err == nil {
		t.Fatal("a disabled share ran claude")
	}
	on := Options{Claude: bin, AllowShare: true}
	res, err := Share(context.Background(), on, ShareRequest{URL: link, Mode: "people", People: []string{"sam@example.com", "Ada"}, Access: "view"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != "people" || res.Access != "view" || len(res.People) != 2 || res.Message != "Shared with 2 people" || res.Turns != 2 {
		t.Fatalf("result = %+v", res)
	}
	args := runs(t, record)[0]
	if !contains(args, "bypassPermissions") || !contains(args, "--dangerously-skip-permissions") || contains(args, "--permission-prompts") {
		t.Fatalf("a share must bypass permissions: %v", args)
	}
	if p := args[len(args)-1]; !strings.Contains(p, `{"access":"view","action":"share","mode":"people","people":["sam@example.com","Ada"],"url":"`+link+`"}`) {
		t.Fatalf("prompt = %s", p)
	}
	res, err = Share(context.Background(), on, ShareRequest{URL: link, Mode: "people", People: []string{"Ada"}})
	if err != nil || res.Access != "comment" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	res, err = Share(context.Background(), on, ShareRequest{URL: link, Mode: "org"})
	if err != nil || res.Access != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	r := runs(t, record)
	if p := r[len(r)-1][len(r[len(r)-1])-1]; !strings.Contains(p, `{"action":"share","mode":"org","url":"`+link+`"}`) {
		t.Fatalf("prompt = %s", p)
	}
	// Publishing never bypasses.
	if _, err := Publish(context.Background(), Options{Claude: fakeBin(t, linkOK), AllowShare: true}, Request{Path: page(t, "p.html")}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		req  ShareRequest
		code string
	}{
		{ShareRequest{URL: link, Mode: "public"}, "invalid-args"},
		{ShareRequest{URL: link, Mode: "org", People: []string{"Ada"}}, "invalid-args"},
		{ShareRequest{URL: link, Mode: "org", Access: "view"}, "invalid-args"},
		{ShareRequest{URL: link, Mode: "people"}, "invalid-args"},
		{ShareRequest{URL: link, Mode: "people", People: []string{"Ada"}, Access: "edit"}, "invalid-args"},
		{ShareRequest{URL: link, Mode: "people", People: []string{"a\nb"}}, "invalid-args"},
		{ShareRequest{URL: "nope", Mode: "org"}, "invalid-url"},
	} {
		_, err := Share(context.Background(), on, c.req)
		wantRefusal(t, err, c.code)
	}
	bin, _ = fakeClaude(t, map[string]string{"share": `{"type":"result","subtype":"success","structured_output":{"shared":false,"message":"","error":"you do not own this artifact"}}`}, 0, "")
	_, err = Share(context.Background(), Options{Claude: bin, AllowShare: true}, ShareRequest{URL: link, Mode: "org"})
	wantRefusal(t, err, "share-refused")
}

func fakeBin(t *testing.T, stdout string) string {
	t.Helper()
	bin, record := fakeClaude(t, map[string]string{"": stdout}, 0, "")
	t.Cleanup(func() {
		for _, args := range runs(t, record) {
			if contains(args, "bypassPermissions") {
				t.Errorf("a run bypassed permissions: %v", args)
			}
		}
	})
	return bin
}

func TestOptionsFromEnv(t *testing.T) {
	env := map[string]string{"ARTIFACT_HELPER_CLAUDE": "/x/claude", "ARTIFACT_HELPER_MODEL": "opus",
		"ARTIFACT_HELPER_TIMEOUT": "90s", "ARTIFACT_HELPER_MAX_BUDGET_USD": "0", "ARTIFACT_HELPER_ALLOW_SHARE": "1"}
	o, err := OptionsFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if o.Claude != "/x/claude" || o.Model != "opus" || o.Timeout != 90*time.Second || o.budget() > 0 || !o.AllowShare {
		t.Fatalf("options = %+v", o)
	}
	if args := Args(o, "Artifact", "{}", "p", false); contains(args, "--max-budget-usd") {
		t.Fatalf("a zero budget must pass no cap: %v", args)
	}
	for k, v := range map[string]string{"ARTIFACT_HELPER_TIMEOUT": "soon", "ARTIFACT_HELPER_MAX_BUDGET_USD": "-3"} {
		_, err := OptionsFromEnv(func(kk string) string {
			if kk == k {
				return v
			}
			return ""
		})
		if r, ok := IsRefusal(err); !ok || r.Code != "invalid-env" {
			t.Errorf("%s=%s: err = %v", k, v, err)
		}
	}
	o, _ = OptionsFromEnv(func(string) string { return "" })
	if o.model() != DefaultModel || o.timeout() != DefaultTimeout || o.budget() != DefaultMaxBudgetUSD || o.AllowShare {
		t.Fatalf("defaults = %+v", o)
	}
}

func TestDoctor(t *testing.T) {
	bin, _ := fakeClaude(t, map[string]string{"": ""}, 0, "")
	h, err := Doctor(context.Background(), Options{Claude: bin})
	if err != nil {
		t.Fatal(err)
	}
	if !h.LoggedIn || h.AuthMethod != "claude.ai" || h.Version != "9.9.9 (Claude Code)" || h.Claude != bin || h.Email != "me@example.com" || h.Organization != "Example Inc" || h.Account() != "me@example.com in Example Inc" {
		t.Fatalf("health = %+v", h)
	}
	authStatus = `{"loggedIn": false}`
	t.Cleanup(func() {
		authStatus = `{"loggedIn": true, "authMethod": "claude.ai", "email": "me@example.com", "orgName": "Example Inc"}`
	})
	bin, _ = fakeClaude(t, map[string]string{"": ""}, 0, "")
	h, err = Doctor(context.Background(), Options{Claude: bin})
	if r, ok := IsRefusal(err); !ok || r.Code != "not-logged-in" || h == nil || h.LoggedIn {
		t.Fatalf("health = %+v, err = %v", h, err)
	}
	if _, err := Doctor(context.Background(), Options{Claude: "/no/such/claude"}); err == nil {
		t.Fatal("a missing binary passed")
	}
}

func TestValidURL(t *testing.T) {
	for s, want := range map[string]bool{
		"https://claude.ai/artifact/Abc_12-3": true, "https://claude.ai/code/artifact/0f1e2d3c": true,
		"http://claude.ai/artifact/x": false, "https://claude.ai/chat/x": false, "https://claude.ai/artifact/": false,
		"https://claude.ai.evil.com/artifact/x": false,
	} {
		if ValidURL(s) != want {
			t.Errorf("ValidURL(%q) = %v", s, !want)
		}
	}
}
