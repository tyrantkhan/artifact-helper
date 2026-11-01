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

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
)

func fakeClaude(t *testing.T, stdout string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo '9.9.9'; exit 0;; auth) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\n" +
		"cat <<'STDOUT'\n" + stdout + "\nSTDOUT\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
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

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsAreListedWithSchemas(t *testing.T) {
	sess := connect(t, headless.Options{})
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Name == "publish_artifact" {
			b, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(b), `"required":["file_path"]`) {
				t.Errorf("publish_artifact requires %s", b)
			}
		}
	}
	if strings.Join(names, ",") != "check_setup,publish_artifact" {
		t.Fatalf("tools = %v", names)
	}
}

func TestPublishArtifactReturnsTheLink(t *testing.T) {
	bin := fakeClaude(t, `{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.05,"structured_output":{"url":"https://claude.ai/artifact/Abc123","error":""}}`)
	p := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(p, []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := connect(t, headless.Options{Claude: bin})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish_artifact", Arguments: map[string]any{"file_path": p, "icon": "chart"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("error: %s", text(res))
	}
	if want := "Published " + p + " as https://claude.ai/artifact/Abc123"; text(res) != want {
		t.Fatalf("text = %q, want %q", text(res), want)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if want := `{"cost_usd":0.05,"turns":2,"updated":false,"url":"https://claude.ai/artifact/Abc123"}`; string(b) != want {
		t.Fatalf("structured = %s, want %s", b, want)
	}
}

func TestPublishArtifactRefusalIsJSON(t *testing.T) {
	sess := connect(t, headless.Options{Claude: fakeClaude(t, "")})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish_artifact", Arguments: map[string]any{"file_path": "/no/such/page.html"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a missing file published")
	}
	var r headless.Refusal
	if err := json.Unmarshal([]byte(text(res)), &r); err != nil || r.Code != "invalid-file" || r.Fix == "" {
		t.Fatalf("text = %q (%v)", text(res), err)
	}
	// A missing argument is refused by the schema, before the handler.
	res, err = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish_artifact", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "file_path") {
		t.Fatalf("no file_path: isError=%v %q", res.IsError, text(res))
	}
}

func TestCheckSetup(t *testing.T) {
	bin := fakeClaude(t, "")
	sess := connect(t, headless.Options{Claude: bin, Model: "opus"})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "check_setup", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("error: %s", text(res))
	}
	sc, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(sc), `"loggedIn":true`) || !strings.Contains(string(sc), `"model":"opus"`) || !strings.Contains(text(res), "signed in (claude.ai)") {
		t.Fatalf("structured = %s, text = %q", sc, text(res))
	}
	sess = connect(t, headless.Options{Claude: "/no/such/claude"})
	res, err = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "check_setup", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), `"code":"no-claude"`) {
		t.Fatalf("missing binary: isError=%v %q", res.IsError, text(res))
	}
}
