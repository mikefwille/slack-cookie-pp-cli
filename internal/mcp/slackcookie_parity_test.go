package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"slack-cookie-pp-cli/internal/cli"
	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/mcp/cobratree"
)

func TestSlackCLIAndTypedAndCommandMCPShareCollections(t *testing.T) {
	for _, k := range []string{"SLACK_COOKIE_DATA_DIR", "SLACK_COOKIE_CONFIG_DIR", "SLACK_WORKSPACE", "SLACK_XOXC_TOKEN", "SLACK_D_COOKIE", "SLACK_D_S_COOKIE", "SLACK_ACCOUNT", "SLACK_COOKIE_CONFIG"} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_COOKIE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth.test":
			fmt.Fprint(w, `{"ok":true,"team_id":"T1","user_id":"U1"}`)
		case "/conversations.history":
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000300","text":"needle"},{"ts":"1700000000.000200","text":"needle","thread_ts":"1700000000.000100"},{"ts":"1700000000.000100","text":"needle","thread_ts":"1700000000.000100","reply_count":1}],"has_more":false}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	if err := config.SaveAccount("work", config.Account{Workspace: srv.URL, Xoxc: "xoxc-fixture", DCookie: "fixture"}, true); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		root := cli.RootCmd()
		var b bytes.Buffer
		root.SetOut(&b)
		root.SetErr(&b)
		root.SetArgs(args)
		err := root.ExecuteContext(t.Context())
		return b.String(), err
	}
	if _, err := run("conversations", "history", "C1", "--json", "--rate-limit", "0"); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "slack-cookie-pp-cli")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/slack-cookie-pp-cli")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture companion build: %v %s", err, out)
	}
	typed := server.NewMCPServer("fixture", "0")
	RegisterTools(typed)
	// Exercise the runtime shell-out adapter against the same real command.
	// Production uses the richer typed tool for this endpoint, so remove only
	// its classification in this test's independent Cobra tree.
	root := cli.RootCmd()
	cmd, _, err := root.Find([]string{"conversations", "history"})
	if err != nil {
		t.Fatal(err)
	}
	delete(cmd.Annotations, cobratree.EndpointAnnotation)
	cmd.Parent().Hidden = false
	shell := server.NewMCPServer("fixture-shell", "0")
	cobratree.RegisterAll(shell, root, func() (string, error) { return bin, nil })
	decode := func(s string) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("invalid envelope %v %s", err, s)
		}
		return v
	}
	invoke := func(s *server.MCPServer, name string, args map[string]any) map[string]any {
		t.Helper()
		if s == shell {
			args = shellParams(args)
		}
		tool := s.GetTool(name)
		if tool == nil {
			t.Fatalf("missing %s", name)
		}
		result, err := tool.Handler(context.Background(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: args}})
		if err != nil || result.IsError {
			t.Fatalf("MCP %s: %v %s", name, err, mcpTextContent(t, result))
		}
		return decode(mcpTextContent(t, result))
	}
	p := map[string]any{"channel": "C1", "data_source": "local", "limit": float64(1), "oldest": "1700000000.000100", "latest": "1700000000.000300", "inclusive": true}
	first, err := run("conversations", "history", "C1", "--data-source", "local", "--limit", "1", "--oldest", "1700000000.000100", "--latest", "1700000000.000300", "--inclusive", "--json")
	if err != nil {
		t.Fatal(err)
	}
	want := decode(first)
	for name, got := range map[string]map[string]any{"typed": invoke(typed, "conversations_history", p), "command": invoke(shell, "conversations_history", p)} {
		if !reflect.DeepEqual(got["results"], want["results"]) {
			t.Fatalf("%s identities/order differ: %v %v", name, got, want)
		}
		wm, gm := want["meta"].(map[string]any), got["meta"].(map[string]any)
		for _, key := range []string{"workspace_id", "has_more", "next_cursor", "coverage"} {
			if !reflect.DeepEqual(wm[key], gm[key]) {
				t.Fatalf("%s %s differs: %v %v", name, key, wm, gm)
			}
		}
	}
	cursor := want["meta"].(map[string]any)["next_cursor"].(string)
	p["cursor"] = cursor
	second := invoke(typed, "conversations_history", p)
	if len(second["results"].([]any)) != 1 {
		t.Fatalf("lost continuation: %v", second)
	}
	// Shared local filter errors must remain visible through both adapters.
	p["cursor"] = "upstream"
	for name, s := range map[string]*server.MCPServer{"typed": typed, "command": shell} {
		args := p
		if s == shell {
			args = shellParams(p)
		}
		result, err := s.GetTool("conversations_history").Handler(t.Context(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: args}})
		if err != nil || !result.IsError {
			t.Fatalf("%s swallowed scope error: %v %v", name, err, result)
		}
	}
	tool := typed.GetTool("conversations_replies").Tool
	for _, key := range []string{"data_source", "all", "oldest", "latest", "inclusive"} {
		if _, ok := tool.InputSchema.Properties[key]; !ok {
			t.Fatalf("typed replies schema missing %s", key)
		}
	}
}

func shellParams(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for key, value := range p {
		if key == "data_source" {
			key = "data-source"
		}
		out[key] = value
	}
	return out
}
