package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/store"
)

func tsFor(t time.Time) string { return fmt.Sprintf("%d.000100", t.Unix()) }

// seedWork configures a default "work" account and writes message rows into its
// per-account mirror so the offline commands have data to read.
func seedWork(t *testing.T, items []json.RawMessage) {
	t.Helper()
	if err := config.SaveAccount("work", config.Account{Workspace: "example", Xoxc: "x", DCookie: "d"}, true); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(accountDBPath("work"))
	if err != nil {
		t.Fatal(err)
	}
	id := store.SlackIdentity{WorkspaceID: "T_FIXTURE", UserID: "UME"}
	if err = s.BindSlackIdentity(id); err != nil {
		t.Fatal(err)
	}
	for i, raw := range items {
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		if extractMessageChannel(obj) == "" {
			obj["channel"] = "C_FIXTURE"
		}
		items[i], _ = json.Marshal(obj)
	}
	err = s.CommitSlackPage(t.Context(), id, "", "/conversations.history", "", items, store.SlackScan{})
	_ = s.Close()
	if err != nil {
		t.Fatalf("seed verified canonical messages: %v", err)
	}
}

func msg(fields map[string]any) json.RawMessage {
	b, _ := json.Marshal(fields)
	return b
}

func TestValidateReadOnlySQL(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select resource_type, count(*) from resources group by 1",
		"WITH t AS (SELECT 1 AS n) SELECT n FROM t",
		// Write-verb words inside string literals / functions must NOT be
		// rejected — mode=ro is the write backstop, not a keyword denylist.
		"SELECT data FROM resources WHERE data LIKE '%update%'",
		"SELECT replace(data, 'drop', 'x') FROM resources",
	}
	for _, q := range ok {
		if _, err := validateReadOnlySQL(q); err != nil {
			t.Errorf("expected %q allowed, got %v", q, err)
		}
	}
	bad := []string{
		"INSERT INTO resources VALUES (1)",
		"UPDATE resources SET id=1",
		"DELETE FROM resources",
		"DROP TABLE resources",
		"ATTACH DATABASE 'x.db' AS y",
		"PRAGMA table_info(resources)",
		"SELECT 1; SELECT 2",
		"SELECT 1; DROP TABLE resources",
		"",
	}
	for _, q := range bad {
		if _, err := validateReadOnlySQL(q); err == nil {
			t.Errorf("expected %q rejected", q)
		}
	}
}

func TestSQLCommandQueriesMirror(t *testing.T) {
	isolateAuthHome(t)
	now := time.Now()
	seedWork(t, []json.RawMessage{
		msg(map[string]any{"ts": tsFor(now), "text": "a", "user": "U1"}),
		msg(map[string]any{"ts": tsFor(now.Add(-time.Hour)), "text": "b", "user": "U2"}),
	})
	out, _, err := runCLI(t, "", "sql", "SELECT count(*) AS n FROM slack_messages", "--json")
	if err != nil {
		t.Fatalf("sql: %v", err)
	}
	if !strings.Contains(out, `"n"`) || !strings.Contains(out, "2") {
		t.Errorf("expected count 2, got: %s", out)
	}
}

func TestSQLCommandRejectsWrite(t *testing.T) {
	isolateAuthHome(t)
	seedWork(t, []json.RawMessage{msg(map[string]any{"ts": tsFor(time.Now()), "text": "x"})})
	_, _, err := runCLI(t, "", "sql", "DELETE FROM resources")
	if ExitCode(err) != 2 {
		t.Fatalf("write SQL should be a usage error (exit 2), got %d (%v)", ExitCode(err), err)
	}
}

func TestDigestCountsMessages(t *testing.T) {
	isolateAuthHome(t)
	now := time.Now()
	seedWork(t, []json.RawMessage{
		msg(map[string]any{"ts": tsFor(now.Add(-time.Hour)), "text": "recent", "user": "U1", "channel": "C1"}),
		msg(map[string]any{"ts": tsFor(now.Add(-50 * time.Hour)), "text": "old", "user": "U1", "channel": "C1"}),
	})
	out, _, err := runCLI(t, "", "digest", "--since", "1d", "--json")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	var dig struct {
		Accounts []struct {
			Messages int `json:"messages"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out), &dig); err != nil {
		t.Fatalf("parse: %v (out=%s)", err, out)
	}
	if len(dig.Accounts) != 1 || dig.Accounts[0].Messages != 1 {
		t.Errorf("expected 1 message within 1d window, got: %s", out)
	}
}

func TestMissedFindsMentionsAndDMs(t *testing.T) {
	isolateAuthHome(t)
	now := time.Now()
	seedWork(t, []json.RawMessage{
		msg(map[string]any{"ts": tsFor(now.Add(-time.Hour)), "text": "hey <@UME> look", "user": "U1", "channel": "C1"}),
		msg(map[string]any{"ts": tsFor(now.Add(-2 * time.Hour)), "text": "a dm", "user": "U2", "channel": "D9"}),
		msg(map[string]any{"ts": tsFor(now.Add(-3 * time.Hour)), "text": "unrelated", "user": "U3", "channel": "C1"}),
	})
	out, _, err := runCLI(t, "", "missed", "--me", "UME", "--since", "1d", "--json")
	if err != nil {
		t.Fatalf("missed: %v", err)
	}
	var res struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse: %v (out=%s)", err, out)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("expected mention + DM (2), got %d: %s", len(res.Messages), out)
	}
	for _, m := range res.Messages {
		if strings.Contains(m.Text, "unrelated") {
			t.Errorf("unrelated channel message should be excluded: %s", out)
		}
	}
}

func TestStaleThreadsFindsOldParticipated(t *testing.T) {
	isolateAuthHome(t)
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	seedWork(t, []json.RawMessage{
		// Stale thread: root + reply, both old, UME participated.
		msg(map[string]any{"ts": tsFor(old), "thread_ts": tsFor(old), "text": "root", "user": "UME", "reply_count": 1, "channel": "C1"}),
		msg(map[string]any{"ts": tsFor(old.Add(time.Minute)), "thread_ts": tsFor(old), "text": "reply", "user": "U2"}),
		// Fresh thread: recent, should be excluded.
		msg(map[string]any{"ts": tsFor(now), "thread_ts": tsFor(now), "text": "fresh", "user": "UME", "reply_count": 1, "channel": "C2"}),
		msg(map[string]any{"ts": tsFor(now.Add(time.Minute)), "thread_ts": tsFor(now), "text": "freshreply", "user": "U2"}),
	})
	out, _, err := runCLI(t, "", "stale-threads", "--days", "7", "--me", "UME", "--json")
	if err != nil {
		t.Fatalf("stale-threads: %v", err)
	}
	var parsed struct {
		Stale []struct {
			ThreadTS string `json:"thread_ts"`
		} `json:"stale"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse: %v (out=%s)", err, out)
	}
	if len(parsed.Stale) != 1 || parsed.Stale[0].ThreadTS != tsFor(old) {
		t.Errorf("expected exactly the old thread stale, got: %s", out)
	}
}

func TestTrendsBucketsByWeek(t *testing.T) {
	isolateAuthHome(t)
	now := time.Now()
	seedWork(t, []json.RawMessage{
		msg(map[string]any{"ts": tsFor(now), "text": "a", "channel": "C1"}),
		msg(map[string]any{"ts": tsFor(now.Add(-8 * 24 * time.Hour)), "text": "b", "channel": "C1"}),
	})
	out, _, err := runCLI(t, "", "trends", "--weeks", "4", "--json")
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if !strings.Contains(out, `"C1"`) {
		t.Errorf("expected C1 series, got: %s", out)
	}
}

func TestOfflineMissingMirrorIsNotAnError(t *testing.T) {
	isolateAuthHome(t)
	// Configure an account but never seed its DB.
	_ = config.SaveAccount("ghost", config.Account{Workspace: "example", Xoxc: "x", DCookie: "d"}, true)
	out, _, err := runCLI(t, "", "digest", "--account", "ghost")
	if err != nil {
		t.Fatalf("missing mirror should not error, got %v", err)
	}
	if !strings.Contains(out, "no local mirror") {
		t.Errorf("expected missing-mirror hint, got: %s", out)
	}
}

func TestSlackOfflineReportsAndSearchExcludeLegacyAndRecognizeGroupDM(t *testing.T) {
	isolateAuthHome(t)
	seedWork(t, []json.RawMessage{msg(map[string]any{"ts": tsFor(time.Now().Add(-time.Hour)), "channel": "GROUP", "text": "verified needle", "user": "U2"})})
	s, err := store.Open(accountDBPath("work"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.SlackIdentity()
	if err = s.CommitSlackDirectoryPage(t.Context(), id, "conversations", "", []json.RawMessage{msg(map[string]any{"id": "GROUP", "is_mpim": true})}, store.SlackScan{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.UpsertBatch("messages", []json.RawMessage{msg(map[string]any{"ts": tsFor(time.Now()), "text": "ambiguous needle", "user": "U3"})}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, args := range [][]string{{"digest", "--since", "1d"}, {"missed", "--since", "1d"}, {"trends", "--weeks", "1"}, {"stale-threads", "--days", "1"}, {"search", "needle"}} {
		args = append(args, "--data-source", "local", "--json")
		out, _, err := runCLI(t, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(out, "ambiguous needle") {
			t.Fatalf("legacy leaked through %v: %s", args, out)
		}
		if !strings.Contains(out, "legacy_identity_unverified") {
			t.Fatalf("legacy exclusion hidden %v: %s", args, out)
		}
		if args[0] == "missed" && !strings.Contains(out, "verified needle") {
			t.Fatalf("group DM missing: %s", out)
		}
	}
	if err = config.SaveAccount("missing", config.Account{Workspace: "other", Xoxc: "x", DCookie: "d"}, false); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "digest", "--all-accounts", "--since", "1d", "--data-source", "local", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state": "partial"`) || !strings.Contains(out, "mirror_unavailable") {
		t.Fatalf("cross-account gap hidden: %s", out)
	}
}
