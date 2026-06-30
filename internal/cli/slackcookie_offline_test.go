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
	stored, fails, err := s.UpsertBatch("conversations-conversations-history", items)
	_ = s.Close()
	if err != nil || fails != 0 || stored != len(items) {
		t.Fatalf("seed UpsertBatch stored=%d fails=%d err=%v (want %d/0)", stored, fails, err, len(items))
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
	out, _, err := runCLI(t, "", "sql", "SELECT count(*) AS n FROM resources", "--json")
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
