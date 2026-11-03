package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"slack-cookie-pp-cli/internal/config"
)

func fixtureWorkspace(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	isolateAuthHome(t)
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
}

func collection(t *testing.T, out string) []map[string]any {
	t.Helper()
	var body struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("collection response: %v: %s", err, out)
	}
	return body.Results
}

func TestSlackHistoryEnvelopeRetainsChannelAndCachesMessages(t *testing.T) {
	fixtureWorkspace(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000100","text":"hello"}],"has_more":false}`)
	})
	if _, _, err := runCLI(t, "", "conversations", "history", "C1", "--json"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "conversations", "history", "C1", "--data-source", "local", "--json")
	if err != nil {
		t.Fatal(err)
	}
	rows := collection(t, out)
	if len(rows) != 1 || rows[0]["channel"] != "C1" {
		t.Fatalf("want one C1 message from real Slack envelope; got %s", out)
	}
}

func TestSlackConversationPaginationContinuesEmptyCursorPage(t *testing.T) {
	calls := 0
	fixtureWorkspace(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"ok":true,"channels":[],"response_metadata":{"next_cursor":"p2"}}`)
		case "p2":
			fmt.Fprint(w, `{"ok":true,"channels":[{"id":"D1","is_im":true}],"response_metadata":{"next_cursor":""}}`)
		default:
			t.Errorf("unexpected cursor")
		}
	})
	out, _, err := runCLI(t, "", "conversations", "list", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	rows := collection(t, out)
	if calls != 2 || len(rows) != 1 {
		t.Fatalf("empty page must follow next_cursor: calls=%d out=%s", calls, out)
	}
}
