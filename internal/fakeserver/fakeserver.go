// Package fakeserver is a tiny MCP server used by tests and for trying sluice
// without any credentials (`go run ./internal/fakeserver/cmd`).
package fakeserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text  string `json:"text" jsonschema:"text to echo back"`
	Times int    `json:"times,omitempty" jsonschema:"how many times (default 1)"`
}

type idIn struct {
	ID string `json:"id" jsonschema:"item id"`
}

type item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type listOut struct {
	Items []item `json:"items"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// New returns a server with these tools:
//
//	echo        read-only, returns text
//	list_items  read-only, structured output
//	peek        not matched by usual patterns but annotated readOnlyHint=true
//	delete_item destructive
//	fail        always returns isError=true
func New() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fakeserver", Version: "v0.0.1"}, nil)
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo the given text.", Annotations: ro},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			n := in.Times
			if n <= 0 {
				n = 1
			}
			out := ""
			for i := 0; i < n; i++ {
				out += in.Text
			}
			return text(out), nil, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_items", Description: "List items.\nSecond line is hidden in short listings.", Annotations: ro},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listOut, error) {
			return nil, listOut{Items: []item{{"1", "alpha"}, {"2", "beta"}}}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "peek", Description: "Read-only by annotation only.", Annotations: ro},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return text("peeked"), nil, nil
		})

	destructive := true
	mcp.AddTool(s, &mcp.Tool{Name: "delete_item", Description: "Delete an item.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(_ context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
			return text("deleted " + in.ID), nil, nil
		})

	// lookup mirrors its "owner" argument into an Mcp-Param-owner HTTP header
	// (SEP-2243), like GitHub's hosted server. Clients must know the tool's
	// schema before calling it over HTTP, or the server rejects the request.
	s.AddTool(&mcp.Tool{
		Name:        "lookup",
		Description: "Look up an owner (argument is mirrored into an HTTP header).",
		Annotations: ro,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"owner": map[string]any{"type": "string", "x-mcp-header": "owner"}},
			"required":   []string{"owner"},
		},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Owner string `json:"owner"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return nil, err
		}
		return text("owner=" + in.Owner), nil
	})

	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Always fails."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("this tool always fails")
		})
	return s
}
