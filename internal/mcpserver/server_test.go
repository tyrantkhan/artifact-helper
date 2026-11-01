package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tyrantkhan/artifact-helper/internal/headless"
)

const (
	link       = "https://claude.ai/artifact/Abc123"
	threadUUID = "0f1e2d3c-4b5a-6978-8a9b-0c1d2e3f4a5b"
	linkOK     = `{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.05,"structured_output":{"url":"` + link + `","error":""}}`
)

// fakeClaude stands in for the claude binary: it runs pre in the working directory, then prints the stdout
// for the "Task: <op>." marker at the start of the prompt, falling back to the "" key.
func fakeClaude(t *testing.T, outputs map[string]string, pre string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	var b strings.Builder
	b.WriteString("#!/bin/sh\ncase \"$1\" in --version) echo '9.9.9'; exit 0;; auth) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\n")
	b.WriteString("for a; do prompt=$a; done\n" + pre + "\ncase \"$prompt\" in\n")
	for op, out := range outputs {
		if op != "" {
			b.WriteString("  'Task: " + op + ".'*) cat <<'STDOUT'\n" + out + "\nSTDOUT\n;;\n")
		}
	}
	b.WriteString("  *) cat <<'STDOUT'\n" + outputs[""] + "\nSTDOUT\n;;\nesac\n")
	if err := os.WriteFile(bin, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func connect(t *testing.T, opts headless.Options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := New(opts, "test").Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string, string) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	sc, _ := json.Marshal(res.StructuredContent)
	return res, b.String(), string(sc)
}

func wantRefusal(t *testing.T, res *mcp.CallToolResult, text, code string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("no error: %s", text)
	}
	var r headless.Refusal
	if err := json.Unmarshal([]byte(text), &r); err != nil || r.Code != code || r.Fix == "" {
		t.Fatalf("text = %q (%v), want code %s", text, err, code)
	}
}

func TestToolsAreListedWithSchemas(t *testing.T) {
	sess := connect(t, headless.Options{})
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]string{
		"publish_artifact": `"required":["file_path"]`, "read_artifact": `"required":["url"]`, "edit_artifact": `"required":["url","edits"]`,
		"read_comments": `"required":["url"]`, "reply_to_comment": `"required":["url","thread_id","text"]`, "resolve_comment": `"required":["url","thread_id"]`,
		"share_artifact": `"required":["url","mode"]`,
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		b, _ := json.Marshal(tool.InputSchema)
		if want := required[tool.Name]; want != "" && !strings.Contains(string(b), want) {
			t.Errorf("%s schema lacks %s: %s", tool.Name, want, b)
		}
	}
	if strings.Join(names, ",") != "check_setup,edit_artifact,publish_artifact,read_artifact,read_comments,reply_to_comment,resolve_comment,share_artifact" {
		t.Fatalf("tools = %v", names)
	}
}

func TestPublishArtifact(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"publish": linkOK}, "")
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := connect(t, headless.Options{Claude: bin})
	res, text, sc := call(t, sess, "publish_artifact", map[string]any{"file_path": p, "icon": "chart"})
	if res.IsError || text != "Published "+p+" as "+link {
		t.Fatalf("text = %q", text)
	}
	if want := `{"cost_usd":0.05,"turns":2,"updated":false,"url":"` + link + `"}`; sc != want {
		t.Fatalf("structured = %s, want %s", sc, want)
	}
	res, text, _ = call(t, sess, "publish_artifact", map[string]any{"file_path": "/no/such/page.html"})
	wantRefusal(t, res, text, "invalid-file")
	// A missing argument is refused by the schema, before the handler.
	res, text, _ = call(t, sess, "publish_artifact", map[string]any{})
	if !res.IsError || !strings.Contains(text, "file_path") {
		t.Fatalf("no file_path: isError=%v %q", res.IsError, text)
	}
}

func TestReadAndEditArtifact(t *testing.T) {
	readOK := `{"type":"result","subtype":"success","num_turns":2,"structured_output":{"path":"index.html","error":""}}`
	bin := fakeClaude(t, map[string]string{"read": readOK, "publish": linkOK}, `[ -e "$PWD/index.html" ] || printf '<h1>Hello</h1>' > "$PWD/index.html"`)
	sess := connect(t, headless.Options{Claude: bin})
	out := t.TempDir()
	res, text, sc := call(t, sess, "read_artifact", map[string]any{"url": link, "out_dir": out})
	if res.IsError || !strings.Contains(text, "Saved index.html") {
		t.Fatalf("text = %q", text)
	}
	saved := filepath.Join(out, "index.html")
	if sc != `{"path":"index.html","saved_path":"`+saved+`"}` {
		t.Fatalf("structured = %s", sc)
	}
	res, text, sc = call(t, sess, "edit_artifact", map[string]any{"url": link, "file_path": saved, "edits": []map[string]string{{"old_str": "Hello", "new_str": "Hi"}}})
	if res.IsError || sc != `{"applied":1,"path":"`+saved+`","url":"`+link+`"}` {
		t.Fatalf("text=%q structured=%s", text, sc)
	}
	if b, _ := os.ReadFile(saved); string(b) != "<h1>Hi</h1>" {
		t.Fatalf("file = %s", b)
	}
	res, text, sc = call(t, sess, "edit_artifact", map[string]any{"url": link, "edits": []map[string]string{{"old_str": "Hello", "new_str": "Hey"}}})
	if res.IsError || !strings.Contains(sc, `"applied":1`) {
		t.Fatalf("text=%q structured=%s", text, sc)
	}
	var o struct{ Path string }
	_ = json.Unmarshal([]byte(sc), &o)
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(o.Path)) })
	res, text, _ = call(t, sess, "edit_artifact", map[string]any{"url": link, "file_path": saved, "edits": []map[string]string{{"old_str": "nope", "new_str": ""}}})
	wantRefusal(t, res, text, "edit-failed")
	res, text, _ = call(t, sess, "edit_artifact", map[string]any{"url": link, "file_path": saved, "edits": []map[string]string{}})
	wantRefusal(t, res, text, "invalid-args")
}

func TestComments(t *testing.T) {
	listing := `{"type":"result","subtype":"success","structured_output":{"threads":[{"id":"` + threadUUID + `","author":"Sam","text":"Typo","resolved":false,"activated":true,"replies":[]}],"raw":"1 thread","more_cursor":"","error":""}}`
	threadOK := `{"type":"result","subtype":"success","structured_output":{"replied":true,"resolved":false,"message":"posted","error":""}}`
	bin := fakeClaude(t, map[string]string{"comments": listing, "reply": threadOK, "resolve": strings.Replace(strings.Replace(threadOK, `"replied":true`, `"replied":false`, 1), `"resolved":false`, `"resolved":true`, 1)}, "")
	sess := connect(t, headless.Options{Claude: bin})
	res, text, sc := call(t, sess, "read_comments", map[string]any{"url": link})
	if res.IsError || text != "1 comment thread(s) on "+link || !strings.Contains(sc, `"id":"`+threadUUID+`"`) || !strings.Contains(sc, `"raw":"1 thread"`) || strings.Contains(sc, "more_cursor") {
		t.Fatalf("text=%q structured=%s", text, sc)
	}
	res, text, sc = call(t, sess, "reply_to_comment", map[string]any{"url": link, "thread_id": threadUUID, "text": "Fixed"})
	if res.IsError || sc != `{"message":"posted","replied":true,"resolved":false}` {
		t.Fatalf("text=%q structured=%s", text, sc)
	}
	res, text, sc = call(t, sess, "resolve_comment", map[string]any{"url": link, "thread_id": threadUUID})
	if res.IsError || sc != `{"message":"posted","replied":false,"resolved":true}` {
		t.Fatalf("text=%q structured=%s", text, sc)
	}
	res, text, _ = call(t, sess, "reply_to_comment", map[string]any{"url": link, "thread_id": "nope", "text": "Fixed"})
	wantRefusal(t, res, text, "invalid-thread")
	res, text, _ = call(t, sess, "read_comments", map[string]any{"url": "https://example.com"})
	wantRefusal(t, res, text, "invalid-url")
}

func TestShareArtifact(t *testing.T) {
	ok := `{"type":"result","subtype":"success","structured_output":{"shared":true,"message":"Shared with the organization","error":""}}`
	bin := fakeClaude(t, map[string]string{"share": ok}, "")
	sess := connect(t, headless.Options{Claude: bin})
	res, text, _ := call(t, sess, "share_artifact", map[string]any{"url": link, "mode": "org"})
	wantRefusal(t, res, text, "share-disabled")
	sess = connect(t, headless.Options{Claude: bin, AllowShare: true})
	res, text, sc := call(t, sess, "share_artifact", map[string]any{"url": link, "mode": "org"})
	if res.IsError || text != "Shared "+link+" with the organization" || sc != `{"message":"Shared with the organization","shared":true}` {
		t.Fatalf("text=%q structured=%s", text, sc)
	}
	res, text, _ = call(t, sess, "share_artifact", map[string]any{"url": link, "mode": "people", "people": []string{"sam@example.com"}, "access": "view"})
	if res.IsError || text != "Shared "+link+" with 1 people (view)" {
		t.Fatalf("text=%q", text)
	}
	res, text, _ = call(t, sess, "share_artifact", map[string]any{"url": link, "mode": "everyone"})
	wantRefusal(t, res, text, "invalid-args")
}

func TestCheckSetup(t *testing.T) {
	bin := fakeClaude(t, map[string]string{"": ""}, "")
	sess := connect(t, headless.Options{Claude: bin, Model: "opus"})
	res, text, sc := call(t, sess, "check_setup", map[string]any{})
	if res.IsError || !strings.Contains(sc, `"loggedIn":true`) || !strings.Contains(sc, `"model":"opus"`) || !strings.Contains(text, "signed in (claude.ai)") {
		t.Fatalf("structured = %s, text = %q", sc, text)
	}
	sess = connect(t, headless.Options{Claude: "/no/such/claude"})
	res, text, _ = call(t, sess, "check_setup", map[string]any{})
	wantRefusal(t, res, text, "no-claude")
}
