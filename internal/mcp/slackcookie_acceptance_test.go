package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"slack-cookie-pp-cli/internal/cli"
	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/store"
)

type slackAdapterFixture struct {
	t     *testing.T
	typed *server.MCPServer
}

func adapterFixture(t *testing.T, handler http.HandlerFunc) slackAdapterFixture {
	t.Helper()
	for _, k := range []string{"SLACK_COOKIE_DATA_DIR", "SLACK_COOKIE_CONFIG_DIR", "SLACK_WORKSPACE", "SLACK_XOXC_TOKEN", "SLACK_D_COOKIE", "SLACK_D_S_COOKIE", "SLACK_ACCOUNT", "SLACK_COOKIE_CONFIG"} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_COOKIE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth.test" {
			fmt.Fprint(w, `{"ok":true,"team_id":"T1","user_id":"U1"}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	if err := config.SaveAccount("work", config.Account{Workspace: srv.URL, Xoxc: "xoxc-fixture", DCookie: "fixture"}, true); err != nil {
		t.Fatal(err)
	}
	typed := server.NewMCPServer("fixture", "0")
	RegisterTools(typed)
	return slackAdapterFixture{t, typed}
}
func (f slackAdapterFixture) command(args ...string) (map[string]any, error) {
	root := cli.RootCmd()
	var b bytes.Buffer
	root.SetOut(&b)
	root.SetErr(&b)
	root.SetArgs(args)
	err := root.ExecuteContext(f.t.Context())
	if err != nil {
		return nil, err
	}
	return f.decode(b.String()), nil
}
func (f slackAdapterFixture) decode(raw string) map[string]any {
	f.t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		f.t.Fatalf("invalid adapter envelope: %v %s", err, raw)
	}
	return out
}
func (f slackAdapterFixture) call(name string, args map[string]any) map[string]any {
	f.t.Helper()
	result, err := f.typed.GetTool(name).Handler(f.t.Context(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: args}})
	if err != nil || result.IsError {
		f.t.Fatalf("typed %s: %v %s", name, err, mcpTextContent(f.t, result))
	}
	return f.decode(mcpTextContent(f.t, result))
}
func resultStamps(t *testing.T, out map[string]any, thread string) []string {
	t.Helper()
	stamps := []string{}
	for _, raw := range out["results"].([]any) {
		row := raw.(map[string]any)
		if row["channel"] != "C1" || row["workspace_id"] != "T1" || (thread != "" && row["thread_ts"] != thread) {
			t.Fatalf("unscoped identity %v", row)
		}
		stamps = append(stamps, row["ts"].(string))
	}
	return stamps
}
func resultMeta(out map[string]any) map[string]any { return out["meta"].(map[string]any) }

func TestSlackLiveInclusiveHistoryFallbackCLIAndTypedMCP(t *testing.T) {
	all := []string{"1700000000.000400", "1700000000.000300", "1700000000.000200", "1700000000.000100"}
	var mu sync.Mutex
	queries := []string{}
	f := adapterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			return
		}
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, q.Get("oldest")+"|"+q.Get("latest")+"|"+q.Get("inclusive"))
		mu.Unlock()
		inclusive := q.Get("inclusive") == "true"
		filtered := []map[string]any{}
		for _, ts := range all {
			lo, hi := store.CompareSlackTS(ts, q.Get("oldest")), store.CompareSlackTS(ts, q.Get("latest"))
			if (lo > 0 || (inclusive && lo == 0)) && (hi < 0 || (inclusive && hi == 0)) {
				filtered = append(filtered, map[string]any{"ts": ts, "text": "needle"})
			}
		}
		more := len(filtered) > 2
		if more {
			filtered = filtered[:2]
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": filtered, "has_more": more})
	})
	cliOut, err := f.command("conversations", "history", "C1", "--all", "--inclusive", "--oldest", all[3], "--latest", all[0], "--limit", "2", "--json", "--rate-limit", "0")
	if err != nil {
		t.Fatal(err)
	}
	typed := f.call("conversations_history", map[string]any{"channel": "C1", "all": true, "inclusive": true, "oldest": all[3], "latest": all[0], "limit": float64(2)})
	for name, out := range map[string]map[string]any{"CLI": cliOut, "typed": typed} {
		if got := resultStamps(t, out, ""); !reflect.DeepEqual(got, all) {
			t.Fatalf("%s inclusive bounds lost or duplicated: %v", name, got)
		}
		meta := resultMeta(out)
		if meta["has_more"] != false || meta["coverage"].(map[string]any)["state"] != "complete" {
			t.Fatalf("%s coverage %v", name, meta)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{all[3] + "|" + all[0] + "|true", "1700000000.000099|1700000000.000300|false", all[3] + "|" + all[0] + "|true", "1700000000.000099|1700000000.000300|false"}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("original inclusive range changed: %v", queries)
	}
}

func TestSlackDurableThreadArchiveCLIAndTypedMCP(t *testing.T) {
	const root = "1700000000.000100"
	pages := []string{}
	expired := false
	f := adapterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.list":
			fmt.Fprint(w, `{"ok":true,"channels":[{"id":"C1"}],"response_metadata":{"next_cursor":""}}`)
		case "/conversations.history":
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000600","text":"needle other root"},{"ts":"1700000000.000200","text":"needle other root"},{"ts":"1700000000.000100","text":"needle root","reply_count":3}],"has_more":false}`)
		case "/conversations.replies":
			cursor := r.URL.Query().Get("cursor")
			pages = append(pages, cursor)
			switch cursor {
			case "":
				fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000100","text":"needle root","reply_count":3},{"ts":"1700000000.000300","text":"needle first"}],"response_metadata":{"next_cursor":"empty"}}`)
			case "empty":
				fmt.Fprint(w, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":"last"}}`)
			case "last":
				if !expired {
					expired = true
					fmt.Fprint(w, `{"ok":false,"error":"invalid_cursor"}`)
				} else {
					fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000400","text":"needle second"},{"ts":"1700000000.000500","text":"needle upper"}],"has_more":false}`)
				}
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	})
	complete := false
	for i := 0; i < 8; i++ {
		out, err := f.command("sync", "--resources", "conversations,messages", "--max-pages", "1", "--json", "--rate-limit", "0")
		if err != nil {
			t.Fatal(err)
		}
		if out["result"].(map[string]any)["coverage"].(map[string]any)["state"] == "complete" {
			complete = true
			break
		}
	}
	if !complete || strings.Join(pages, ",") != ",empty,last,,empty,last" {
		t.Fatalf("durable thread stalled: %v", pages)
	}
	cliOut, err := f.command("conversations", "replies", "C1", root, "--data-source", "local", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	typed := f.call("conversations_replies", map[string]any{"channel": "C1", "ts": root, "data_source": "local", "all": true})
	want := []string{root, "1700000000.000300", "1700000000.000400", "1700000000.000500"}
	for name, out := range map[string]map[string]any{"CLI": cliOut, "typed": typed} {
		if got := resultStamps(t, out, root); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s root/replay/ascending: %v", name, got)
		}
	}
	// The adapter's narrower exclusive range is certified by the same archive.
	cliOut, err = f.command("conversations", "replies", "C1", root, "--data-source", "local", "--oldest", want[1], "--latest", want[3], "--json")
	if err != nil {
		t.Fatal(err)
	}
	typed = f.call("conversations_replies", map[string]any{"channel": "C1", "ts": root, "data_source": "local", "oldest": want[1], "latest": want[3]})
	for name, out := range map[string]map[string]any{"CLI": cliOut, "typed": typed} {
		if got := resultStamps(t, out, root); !reflect.DeepEqual(got, want[2:3]) {
			t.Fatalf("%s exclusive bounds %v", name, got)
		}
		if resultMeta(out)["coverage"].(map[string]any)["state"] != "complete" {
			t.Fatalf("%s narrow coverage %v", name, out)
		}
	}
	// Default false and omitted inclusive must produce interchangeable cursors.
	for _, kind := range []string{"history", "replies", "search"} {
		args := []string{"conversations", kind, "C1"}
		p := map[string]any{"channel": "C1", "data_source": "local", "limit": float64(1)}
		tool := "conversations_" + kind
		if kind == "replies" {
			args = append(args, root)
			p["ts"] = root
		}
		if kind == "search" {
			args = []string{"search", "needle", "--channel", "C1"}
			tool = "search"
			p["query"] = "needle"
		}
		args = append(args, "--data-source", "local", "--limit", "1", "--json")
		first, err := f.command(args...)
		if err != nil {
			t.Fatal(err)
		}
		firstTyped := f.call(tool, p)
		cursor := resultMeta(first)["next_cursor"].(string)
		if cursor != resultMeta(firstTyped)["next_cursor"] {
			t.Fatalf("%s default scopes diverge", kind)
		}
		p["cursor"] = cursor
		second := f.call(tool, p)
		args = append(args, "--cursor", resultMeta(firstTyped)["next_cursor"].(string))
		secondCLI, err := f.command(args...)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(second["results"], secondCLI["results"]) {
			t.Fatalf("%s cross-adapter continuation differs", kind)
		}
	}
}

func TestSlackMCPRejectsInvalidPageLimitsBeforeSourceReads(t *testing.T) {
	calls := 0
	f := adapterFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Errorf("invalid input reached %s", r.URL.Path)
	})
	for _, all := range []bool{false, true} {
		for _, value := range []any{float64(0), float64(-1), float64(1.5), "invalid"} {
			for _, name := range []string{"conversations_history", "messages_search"} {
				key := "limit"
				p := map[string]any{"channel": "C1", "all": all}
				if name == "messages_search" {
					key = "count"
					p["query"] = "needle"
				}
				p[key] = value
				result, err := f.typed.GetTool(name).Handler(t.Context(), mcplib.CallToolRequest{Params: mcplib.CallToolParams{Arguments: p}})
				if err != nil || !result.IsError || !strings.Contains(mcpTextContent(t, result), key+" must be positive") {
					t.Fatalf("%s %v invalid limit accepted: %v %v", name, value, err, result)
				}
			}
		}
	}
	if calls != 0 {
		t.Fatal("invalid MCP input triggered collection")
	}
	if _, err := f.command("conversations", "history", "C1", "--limit", "0", "--json"); err == nil {
		t.Fatal("CLI accepted zero limit")
	}
}
