// Package mcpserver is the MCP server: one tool that publishes a file as a claude.ai artifact, and one that
// checks the setup. It speaks MCP over stdio, so any MCP client can launch it.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tyrantkhan/claude-artifact-helper/internal/headless"
)

// Name is the server's name as MCP clients see it.
const Name = "claude-artifact-helper"

// PublishInput is the publish_artifact tool's input.
type PublishInput struct {
	FilePath    string `json:"file_path" jsonschema:"Absolute path of the .html (or .md) file to publish. The file must already exist; write it first."`
	Title       string `json:"title,omitempty" jsonschema:"Fallback title when the page has no <title>: a name of a few words, kept the same on every republish."`
	Description string `json:"description,omitempty" jsonschema:"One sentence for the subtitle on the gallery card."`
	Icon        string `json:"icon,omitempty" jsonschema:"One short generic word for the browser-tab icon, such as chart, map, recipe or code. Default page. Omit on an update to keep the icon."`
	URL         string `json:"url,omitempty" jsonschema:"An existing artifact's claude.ai link to update in place. Omit to create a new artifact."`
}

// PublishOutput is the publish_artifact tool's result.
type PublishOutput struct {
	URL     string  `json:"url" jsonschema:"The artifact's claude.ai link, private to the account until shared."`
	Updated bool    `json:"updated" jsonschema:"True when an existing artifact was republished."`
	Turns   int     `json:"turns" jsonschema:"Model turns the headless run took."`
	CostUSD float64 `json:"cost_usd" jsonschema:"Cost of the run as Claude Code reports it; notional on a subscription."`
}

// New builds the server. The options decide how the claude binary is run; version is reported to clients.
func New(opts headless.Options, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Title: "Claude artifact helper", Version: version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:  "publish_artifact",
		Title: "Publish a claude.ai artifact",
		Description: "Publish an HTML (or Markdown) file as a claude.ai artifact: a private web page hosted on claude.ai, " +
			"shareable by its link. Runs Claude Code headlessly under the account's Claude subscription. " +
			"Pass url to update an artifact published earlier instead of creating a new one. Takes about a minute. " +
			"A failure comes back as JSON with code, message and fix: follow fix.",
		Annotations: &mcp.ToolAnnotations{Title: "Publish a claude.ai artifact", OpenWorldHint: ptr(true), IdempotentHint: false},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PublishInput) (*mcp.CallToolResult, PublishOutput, error) {
		res, err := headless.Publish(ctx, opts, headless.Request{
			Path: in.FilePath, Title: in.Title, Description: in.Description, Icon: in.Icon, URL: in.URL,
		})
		if err != nil {
			return nil, PublishOutput{}, asToolError(err)
		}
		out := PublishOutput{URL: res.URL, Updated: res.Updated, Turns: res.Turns, CostUSD: res.CostUSD}
		verb := "Published"
		if res.Updated {
			verb = "Updated"
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("%s %s as %s", verb, res.Path, res.URL),
		}}}, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:  "check_setup",
		Title: "Check the artifact helper's setup",
		Description: "Check that Claude Code is installed and signed in to a Claude subscription, which publish_artifact " +
			"needs. Call it when a publish fails with code no-claude or not-logged-in, or before the first publish.",
		Annotations: &mcp.ToolAnnotations{Title: "Check the artifact helper's setup", ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, *headless.Health, error) {
		h, err := headless.Doctor(ctx, opts)
		if err != nil {
			return nil, nil, asToolError(err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("claude %s at %s, signed in (%s); publishes run on %s with a %s timeout", h.Version, h.Claude, h.AuthMethod, h.Model, h.Timeout),
		}}}, h, nil
	})
	return s
}

// asToolError renders a refusal as JSON, so the model on the other side reads its code and fix. The SDK puts
// the error text in the result and marks it isError.
func asToolError(err error) error {
	r, ok := headless.IsRefusal(err)
	if !ok {
		r = &headless.Refusal{Code: "error", Message: err.Error()}
	}
	b, _ := json.Marshal(r)
	return errors.New(string(b))
}

func ptr[T any](v T) *T { return &v }
