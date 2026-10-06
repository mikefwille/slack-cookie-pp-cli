package slackmirror

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/store"
	"strings"
	"testing"
)

func fixtureReader(t *testing.T, h http.HandlerFunc) *Reader {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth.test" {
			fmt.Fprint(w, `{"ok":true,"team_id":"T1","user_id":"U1"}`)
			return
		}
		h(w, r)
	}))
	t.Cleanup(server.Close)
	c := client.New(&config.Config{BaseURL: server.URL, AuthHeaderVal: "Bearer fixture"}, 0, 0)
	c.NoCache = true
	reader, err := Open(t.Context(), c, filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Store.Close() })
	return reader
}

func TestArchiveOwnsPrefixAndRepeatedCursorStopsVisibly(t *testing.T) {
	requests := []string{}
	r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
		cursor := req.URL.Query().Get("cursor")
		requests = append(requests, cursor)
		switch cursor {
		case "suffix":
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000002.000100","text":"suffix"}],"has_more":false}`)
		case "":
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000001.000100","text":"prefix"}],"response_metadata":{"next_cursor":"suffix"}}`)
		}
	})
	p := map[string]string{"channel": "C1", "latest": "1700000099.000100", "cursor": "suffix"}
	if _, err := r.Fetch(t.Context(), "/conversations.history", p, true, false, 0); err != nil {
		t.Fatal(err)
	}
	delete(p, "cursor")
	if _, err := r.Partition(t.Context(), "/conversations.history", p, false, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Join(requests, ",") != "suffix,,suffix" {
		t.Fatalf("ad hoc suffix seeded trusted archive prefix: %v", requests)
	}
	rows, _ := r.Store.QuerySlackMessages(t.Context(), store.SlackQuery{})
	if len(rows) != 2 {
		t.Fatal("prefix missing or overlap duplicated")
	}
	repeated := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":"repeat"}}`)
	})
	_, err := repeated.Partition(t.Context(), "/conversations.history", map[string]string{"channel": "C1"}, false, 0)
	if err == nil || !strings.Contains(err.Error(), "progress") {
		t.Fatalf("repeated cursor not visible: %v", err)
	}
	scan, _ := repeated.Store.SlackScan(ArchiveScope(repeated.Identity, "/conversations.history", map[string]string{"channel": "C1"}))
	if scan.Complete || scan.Cursor != "repeat" || scan.Reason == "" {
		t.Fatalf("unsafe stop: %+v", scan)
	}
}

func TestArchiveDiscoveryMembershipFrozenResumeOldRootRepliesAndCoverage(t *testing.T) {
	latest := ""
	expired := false
	historyCalls := 0
	r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Query()
		switch req.URL.Path {
		case "/conversations.list":
			if p.Get("cursor") == "" {
				fmt.Fprint(w, `{"ok":true,"channels":[{"id":"C1","is_channel":true}],"response_metadata":{"next_cursor":"directory2"}}`)
			} else {
				fmt.Fprint(w, `{"ok":true,"channels":[{"id":"GPRIVATE","is_private":true},{"id":"D1","is_im":true},{"id":"GROUP","is_mpim":true}],"response_metadata":{"next_cursor":""}}`)
			}
		case "/conversations.history":
			historyCalls++
			if p.Get("channel") == "STALE" {
				t.Error("unverified cached channel became archive target")
			}
			if latest == "" {
				latest = p.Get("latest")
			}
			if p.Get("latest") != latest {
				t.Error("resumed frozen range drifted")
			}
			if p.Get("channel") == "C1" {
				if p.Get("cursor") == "history2" && !expired {
					expired = true
					fmt.Fprint(w, `{"ok":false,"error":"invalid_cursor"}`)
					return
				}
				if p.Get("cursor") == "" {
					fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000100","thread_ts":"1700000000.000100","reply_count":1,"text":"old root"}],"response_metadata":{"next_cursor":"history2"}}`)
				} else {
					fmt.Fprint(w, `{"ok":true,"messages":[],"has_more":false}`)
				}
			} else {
				fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1706000000.000100","text":"same timestamp elsewhere"}],"has_more":false}`)
			}
		case "/conversations.replies":
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1706000000.000100","thread_ts":"1700000000.000100","text":"recent reply"}],"has_more":false}`)
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
		}
	})
	if _, _, err := r.Store.UpsertBatch("conversations", []json.RawMessage{json.RawMessage(`{"id":"STALE"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.CommitSlackDirectoryPage(t.Context(), r.Identity, "conversations", "", []json.RawMessage{json.RawMessage(`{"id":"OLD_VERIFIED","is_channel":true}`)}, store.SlackScan{}); err != nil {
		t.Fatal(err)
	}
	// Preserve an ambiguous legacy message to prove re-fetch can complete
	// traversal without exposing or deleting quarantine evidence.
	if _, _, err := r.Store.UpsertBatch("messages", []json.RawMessage{json.RawMessage(`{"ts":"1706000000.000200","text":"legacy must not leak"}`)}); err != nil {
		t.Fatal(err)
	}
	opts := ArchiveOptions{Resources: []string{"conversations", "messages"}, Oldest: "1705000000.000100", Since: "window", MaxPages: 1}
	first, err := r.Archive(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Coverage.State != "partial" || historyCalls != 0 {
		t.Fatal("unfinished discovery traversed/claimed full archive")
	}
	opts.Oldest = "1707000000.000100" // moving relative cutoff must not alter a resumed range.
	var report ArchiveReport
	for i := 0; i < 8; i++ {
		report, err = r.Archive(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if report.Coverage.State == "complete" {
			break
		}
	}
	if report.Coverage.State != "complete" || !report.Resumed || report.Oldest != first.Oldest || report.Latest != first.Latest {
		t.Fatalf("resume/coverage: %+v", report)
	}
	if !expired || report.Conversations != 4 {
		t.Fatalf("expiry/all types not exercised: %+v", report)
	}
	if report.WindowMessages != 4 {
		t.Fatalf("window count included old root or replay: %+v", report)
	}
	outside := CoverageFor(r.Store, r.Identity, "/local.messages", map[string]string{"channel": "OLD_VERIFIED", "oldest": first.Oldest, "latest": first.Latest})
	if outside.State == "complete" {
		t.Fatal("prior directory row certified outside-run channel")
	}
	rows, err := r.Store.QuerySlackMessages(t.Context(), store.SlackQuery{})
	if err != nil || len(rows) != 5 {
		t.Fatalf("canonical identity/replay: %d %v", len(rows), err)
	}
	local, err := Local(t.Context(), r.Store, "/local.search", map[string]string{"query": "recent", "channel": "C1", "oldest": first.Oldest, "latest": first.Latest}, true)
	if err != nil || len(local.Items) != 1 || local.Coverage.State != "complete" {
		t.Fatalf("old-root recent reply or coverage: %v %+v", err, local)
	}
	broad, _ := Local(t.Context(), r.Store, "/local.messages", map[string]string{"oldest": "1600000000.000100", "latest": first.Latest}, true)
	if broad.Coverage.State == "complete" {
		t.Fatal("narrow archive certified broader range")
	}
	if len(report.Coverage.Reasons) == 0 || report.Coverage.LegacyRows != 1 {
		t.Fatal("preserved quarantine warning disappeared")
	}
}

func TestLocalKeysetPagingScopesDeletedCursorInsertionAndChangedFTSMembership(t *testing.T) {
	r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"ok":true,"messages":[],"has_more":false}`)
	})
	put := func(channel, body string) {
		t.Helper()
		if err := r.Store.CommitSlackPage(t.Context(), r.Identity, channel, "/conversations.history", "", []json.RawMessage{json.RawMessage(body)}, store.SlackScan{}); err != nil {
			t.Fatal(err)
		}
	}
	put("C1", `{"ts":"1700000002.000100","text":"needle"}`)
	put("C2", `{"ts":"1700000002.000100","text":"needle"}`)
	put("C1", `{"ts":"1700000001.000100","text":"needle"}`)
	p := map[string]string{"query": "needle", "limit": "1"}
	first, err := Local(t.Context(), r.Store, "/local.search", p, false)
	if err != nil || !first.HasMore {
		t.Fatalf("first page: %v %+v", err, first)
	}
	var row map[string]any
	_ = json.Unmarshal(first.Items[0], &row)
	if row["channel"] != "C2" {
		t.Fatal("equal timestamp tie order not deterministic")
	}
	put("C2", `{"subtype":"message_deleted","deleted_ts":"1700000002.000100"}`)
	put("C3", `{"ts":"1700000001.000200","text":"needle"}`)
	p["cursor"] = first.NextCursor
	second, err := Local(t.Context(), r.Store, "/local.search", p, false)
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("deleted last cursor row failed: %v", err)
	}
	_ = json.Unmarshal(second.Items[0], &row)
	if row["channel"] != "C1" || row["ts"] != "1700000002.000100" {
		t.Fatal("keyset skipped equal timestamp sibling")
	}
	put("C1", `{"ts":"1700000001.000100","text":"changed membership","edited":{"ts":"1700000009.000100"}}`)
	p["cursor"] = second.NextCursor
	third, err := Local(t.Context(), r.Store, "/local.search", p, false)
	if err != nil || len(third.Items) != 1 || third.HasMore {
		t.Fatalf("live insertion/edit membership: %v %+v", err, third)
	}
	p["channel"] = "C1"
	if _, err = Local(t.Context(), r.Store, "/local.search", p, false); err == nil {
		t.Fatal("cross-query cursor accepted")
	}
	delete(p, "channel")
	p["query"] = "another"
	if _, err = Local(t.Context(), r.Store, "/local.search", p, false); err == nil {
		t.Fatal("cross-FTS cursor accepted")
	}
	p["query"] = "needle"
	if _, err = Local(t.Context(), r.Store, "/conversations.replies", map[string]string{"channel": "C1", "ts": "1700000002.000100", "cursor": first.NextCursor}, false); err == nil {
		t.Fatal("cross-order cursor accepted")
	}
	p["cursor"] = "upstream"
	if _, err = Local(t.Context(), r.Store, "/local.search", p, false); err == nil {
		t.Fatal("upstream cursor accepted locally")
	}
}

func TestScopedEmptyCoverageRetentionAndCursorlessReplies(t *testing.T) {
	limited := false
	r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/conversations.replies" {
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000001.000100","thread_ts":"1700000000.000100"}],"has_more":true}`)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"messages":[],"has_more":false,"is_limited":%t}`, limited)
	})
	p := map[string]string{"channel": "C1", "oldest": "1700000000.000100", "latest": "1700000099.000100"}
	empty, err := r.Fetch(t.Context(), "/conversations.history", p, true, false, 0)
	if err != nil || empty.Coverage.State != "complete" {
		t.Fatalf("valid empty not complete: %v %+v", err, empty)
	}
	local, err := Local(t.Context(), r.Store, "/conversations.history", p, true)
	if err != nil || len(local.Items) != 0 || local.Coverage.State != "complete" {
		t.Fatalf("scoped empty: %v %+v", err, local)
	}
	p["oldest"] = "1600000000.000100"
	local, _ = Local(t.Context(), r.Store, "/conversations.history", p, true)
	if local.Coverage.State == "complete" {
		t.Fatal("broader history certified")
	}
	limited = true
	p["channel"] = "C2"
	retained, err := r.Fetch(t.Context(), "/conversations.history", p, true, false, 0)
	if err != nil || retained.Coverage.State == "complete" {
		t.Fatal("is_limited ignored")
	}
	replies, err := r.Fetch(context.Background(), "/conversations.replies", map[string]string{"channel": "C1", "ts": "1700000000.000100"}, true, false, 0)
	if err != nil || replies.Coverage.State == "complete" || !replies.HasMore || replies.NextCursor != "" {
		t.Fatalf("cursorless replies completeness guessed: %v %+v", err, replies)
	}
}
