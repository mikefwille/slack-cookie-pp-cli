package slackmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func reopenReader(t *testing.T, r *Reader) {
	t.Helper()
	path := r.Store.Path()
	r.Store.Close()
	next, err := Open(t.Context(), r.Client, path)
	if err != nil {
		t.Fatal(err)
	}
	*r = *next
}

func TestDirectoryOnlyAndMessageArchiveResumeAcrossReopen(t *testing.T) {
	for _, resources := range [][]string{{"conversations"}, {"users"}, {"conversations", "users"}, {"conversations", "users", "messages"}} {
		t.Run(strings.Join(resources, "-"), func(t *testing.T) {
			seen := map[string][]string{}
			r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
				cursor := req.URL.Query().Get("cursor")
				seen[req.URL.Path] = append(seen[req.URL.Path], cursor)
				key, id := "channels", "C1"
				if req.URL.Path == "/users.list" {
					key, id = "members", "U1"
				}
				if req.URL.Path == "/conversations.history" {
					fmt.Fprint(w, `{"ok":true,"messages":[],"has_more":false}`)
					return
				}
				if cursor == "" {
					fmt.Fprintf(w, `{"ok":true,%q:[{"id":%q}],"response_metadata":{"next_cursor":"p2"}}`, key, id)
				} else {
					fmt.Fprintf(w, `{"ok":true,%q:[],"response_metadata":{"next_cursor":""}}`, key)
				}
			})
			opts := ArchiveOptions{Resources: resources, MaxPages: 1}
			var out ArchiveReport
			var err error
			for i := 0; i < 5; i++ {
				out, err = r.Archive(t.Context(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if out.Coverage.State == "complete" {
					break
				}
				reopenReader(t, r)
			}
			if out.Coverage.State != "complete" {
				t.Fatalf("bounded directory resume stalled: %+v requests=%v", out, seen)
			}
			for _, resource := range resources {
				path := "/conversations.list"
				if resource == "users" {
					path = "/users.list"
				}
				if resource == "messages" {
					continue
				}
				if strings.Join(seen[path], ",") != ",p2" {
					t.Fatalf("%s prefix replayed: %v", path, seen[path])
				}
			}
		})
	}
}

func TestRetentionLimitationSurvivesPageAndResume(t *testing.T) {
	for _, maxPages := range []int{0, 1} {
		t.Run(fmt.Sprint(maxPages), func(t *testing.T) {
			r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Query().Get("cursor") == "" {
					fmt.Fprint(w, `{"ok":true,"messages":[],"is_limited":true,"response_metadata":{"next_cursor":"p2"}}`)
				} else {
					fmt.Fprint(w, `{"ok":true,"messages":[],"has_more":false}`)
				}
			})
			p := map[string]string{"channel": "C1", "latest": "1700000001.000100"}
			first, err := r.Partition(t.Context(), "/conversations.history", p, false, maxPages)
			if err != nil {
				t.Fatal(err)
			}
			if first.Coverage.State != "partial" {
				t.Fatal("limited page certified complete")
			}
			if maxPages == 0 {
				if !strings.Contains(strings.Join(first.Coverage.Reasons, ","), "retention_limited") {
					t.Fatal("same-call limitation lost")
				}
				return
			}
			reopenReader(t, r)
			last, err := r.Partition(t.Context(), "/conversations.history", p, false, 1)
			if err != nil {
				t.Fatal(err)
			}
			if last.Coverage.State != "partial" || !strings.Contains(strings.Join(last.Coverage.Reasons, ","), "retention_limited") {
				t.Fatalf("retention evidence cleared: %+v", last.Coverage)
			}
		})
	}
}

func TestThreadArchivePersistsEmptyPageExpiryReplayAndExactBounds(t *testing.T) {
	expired := false
	interrupted := false
	var interrupt context.CancelFunc
	pages := []string{}
	r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/conversations.list":
			fmt.Fprint(w, `{"ok":true,"channels":[{"id":"C1"}],"response_metadata":{"next_cursor":""}}`)
		case "/conversations.history":
			fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000100","thread_ts":"1700000000.000100","reply_count":3,"text":"root"}],"has_more":false}`)
		case "/conversations.replies":
			cursor := req.URL.Query().Get("cursor")
			pages = append(pages, cursor)
			switch cursor {
			case "":
				fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000300","thread_ts":"1700000000.000100","text":"first"}],"response_metadata":{"next_cursor":"empty"}}`)
			case "empty":
				if !interrupted {
					interrupted = true
					interrupt()
					return
				}
				fmt.Fprint(w, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":"last"}}`)
			case "last":
				if !expired {
					expired = true
					fmt.Fprint(w, `{"ok":false,"error":"invalid_cursor"}`)
				} else {
					fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000400","thread_ts":"1700000000.000100","text":"second"},{"ts":"1700000000.000500","thread_ts":"1700000000.000100","text":"upper boundary"}],"has_more":false}`)
				}
			}
		}
	})
	opts := ArchiveOptions{Resources: []string{"messages"}, Oldest: "1700000000.000200", Since: "fixture", MaxPages: 1}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	interrupt = cancel
	initial := opts
	initial.MaxPages = 0
	if _, err := r.Archive(ctx, initial); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption was not surfaced: %v", err)
	}
	var committed int
	if err := r.Store.DB().QueryRow("SELECT count(*) FROM slack_messages WHERE ts='1700000000.000300'").Scan(&committed); err != nil || committed != 1 {
		t.Fatal("first reply page was not committed before interruption")
	}
	reopenReader(t, r)
	var out ArchiveReport
	var err error
	for i := 0; i < 8; i++ {
		out, err = r.Archive(t.Context(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if out.Coverage.State == "complete" {
			break
		}
		reopenReader(t, r)
	}
	if out.Coverage.State != "complete" || !expired || strings.Join(pages, ",") != ",empty,empty,last,,empty,last" {
		t.Fatalf("thread resume/replay %+v pages=%v", out, pages)
	}
	all, err := Local(t.Context(), r.Store, "/conversations.replies", map[string]string{"channel": "C1", "ts": "1700000000.000100"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 4 {
		t.Fatalf("replay duplicated/dropped root or reply: %s", all.Items)
	}
	bounded, err := Local(t.Context(), r.Store, "/conversations.replies", map[string]string{"channel": "C1", "ts": "1700000000.000100", "oldest": "1700000000.000300", "latest": "1700000000.000500"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.Items) != 1 {
		t.Fatalf("exclusive thread bounds: %s", bounded.Items)
	}
	var row map[string]any
	json.Unmarshal(bounded.Items[0], &row)
	if row["ts"] != "1700000000.000400" || row["channel"] != "C1" || row["thread_ts"] != "1700000000.000100" || row["workspace_id"] != "T1" {
		t.Fatalf("thread identity: %v", row)
	}
	if out.WindowMessages != 3 {
		t.Fatalf("old root/replay inflated requested window: %+v", out)
	}
}

func TestCursorlessThreadReasonIsNotReplacedByPageCap(t *testing.T) {
	for _, cap := range []int{0, 1} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			r := fixtureReader(t, func(w http.ResponseWriter, req *http.Request) {
				fmt.Fprint(w, `{"ok":true,"messages":[{"ts":"1700000000.000200"}],"has_more":true}`)
			})
			out, err := r.Partition(t.Context(), "/conversations.replies", map[string]string{"channel": "C1", "ts": "1700000000.000100"}, false, cap)
			if err != nil || out.Coverage.State != "partial" || !strings.Contains(strings.Join(out.Coverage.Reasons, ","), "cursorless_reply_pagination_unverified") || strings.Contains(strings.Join(out.Coverage.Reasons, ","), "page_limit_reached") {
				t.Fatalf("specific reason lost: %+v %v", out, err)
			}
		})
	}
}

func TestInclusiveTimeFallbackUsesExactPredecessor(t *testing.T) {
	for _, test := range []struct{ bound, want string }{
		{"0.000000", ""}, {"0.000001", "0.000000"}, {"1700000000.000000", "1699999999.999999"},
		{"999999999999999999999999999.000100", "999999999999999999999999999.000099"},
	} {
		if got := previousMicrosecond(test.bound); got != test.want {
			t.Fatalf("%s predecessor %s want %s", test.bound, got, test.want)
		}
	}
}
