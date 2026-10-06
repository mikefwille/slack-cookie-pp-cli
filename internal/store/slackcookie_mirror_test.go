package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func canonicalStore(t *testing.T) (*Store, SlackIdentity) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id := SlackIdentity{WorkspaceID: "T1", UserID: "U1"}
	if err = s.BindSlackIdentity(id); err != nil {
		t.Fatal(err)
	}
	return s, id
}
func rawRows(values ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(values))
	for i, v := range values {
		out[i] = json.RawMessage(v)
	}
	return out
}
func commitRows(t *testing.T, s *Store, id SlackIdentity, channel, source string, values ...string) {
	t.Helper()
	if err := s.CommitSlackPage(t.Context(), id, channel, source, "", rawRows(values...), SlackScan{}); err != nil {
		t.Fatal(err)
	}
}

func TestSlackObserverMismatchDoesNotMutateIdentityDataOrProgress(t *testing.T) {
	s, id := canonicalStore(t)
	commitRows(t, s, id, "C1", "/conversations.history", `{"ts":"1700000000.000100","text":"private"}`)
	if err := s.SaveSlackScan("owned", SlackScan{Cursor: "page2"}); err != nil {
		t.Fatal(err)
	}
	for _, other := range []SlackIdentity{{"T2", "U1"}, {"T1", "U2"}, {"T1", ""}} {
		if err := s.BindSlackIdentity(other); err == nil {
			t.Fatalf("accepted observer %v", other)
		}
		if err := s.CommitSlackPage(t.Context(), other, "C1", "/conversations.history", "owned", rawRows(`{"ts":"1700000000.000200"}`), SlackScan{Complete: true}); err == nil {
			t.Fatal("accepted unbound observer")
		}
	}
	got, _ := s.SlackIdentity()
	if got != id {
		t.Fatalf("binding mutated: %v", got)
	}
	scan, _ := s.SlackScan("owned")
	if scan.Cursor != "page2" || scan.Complete {
		t.Fatalf("progress mutated: %+v", scan)
	}
	rows, err := s.QuerySlackMessages(t.Context(), SlackQuery{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("data mutated: %v %d", err, len(rows))
	}
	if err = s.BindSlackIdentity(id); err != nil {
		t.Fatalf("same observer renewal: %v", err)
	}
}

func TestSlackCanonicalOverlapEditsMetadataRemovalAndTombstones(t *testing.T) {
	s, id := canonicalStore(t)
	original := `{"ts":"1700000000.000100","text":"old","attachments":[{"text":"remove"}],"thread_ts":"1700000000.000100","reply_count":1,"latest_reply":"1700000001.000100"}`
	commitRows(t, s, id, "C1", "/conversations.history", original)
	commitRows(t, s, id, "G1", "/conversations.history", original)
	commitRows(t, s, id, "C1", "/conversations.replies", original)
	edited := `{"ts":"1700000000.000100","text":"new","edited":{"ts":"1700000002.000100"},"thread_ts":"1700000000.000100","reply_count":2,"latest_reply":"1700000003.000100"}`
	commitRows(t, s, id, "C1", "/conversations.history", edited)
	commitRows(t, s, id, "", "/search.messages", `{"ts":"1700000000.000100","text":"old","channel":{"id":"C1"}}`)
	commitRows(t, s, id, "C1", "/conversations.history", `{"ts":"1700000000.000100","text":"new","edited":{"ts":"1700000002.000100"},"thread_ts":"1700000000.000100","reply_count":3,"latest_reply":"1700000004.000100"}`)
	commitRows(t, s, id, "C1", "/conversations.history", `{"ts":"1700000000.000100","text":"new","edited":{"ts":"1700000002.000100"},"reply_count":1}`)
	rows, err := s.QuerySlackMessages(t.Context(), SlackQuery{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("dedupe/collision: %v %d", err, len(rows))
	}
	rows, err = s.QuerySlackMessages(t.Context(), SlackQuery{Channel: "C1", Text: "new"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("FTS edited content: %v %d", err, len(rows))
	}
	var obj map[string]any
	_ = json.Unmarshal(rows[0], &obj)
	if obj["text"] != "new" || obj["attachments"] != nil || obj["reply_count"] != float64(3) {
		t.Fatalf("merge: %s", rows[0])
	}
	var sources string
	if err = s.DB().QueryRow("SELECT sources FROM slack_messages WHERE channel_id='C1'").Scan(&sources); err != nil {
		t.Fatal(err)
	}
	var seen map[string]bool
	_ = json.Unmarshal([]byte(sources), &seen)
	if len(seen) != 3 {
		t.Fatalf("source provenance: %s", sources)
	}
	commitRows(t, s, id, "C1", "/conversations.history", `{"subtype":"message_deleted","deleted_ts":"1700000000.000100","ts":"1700000099.000100"}`)
	commitRows(t, s, id, "C1", "/conversations.replies", edited)
	rows, err = s.QuerySlackMessages(t.Context(), SlackQuery{Channel: "C1"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("tombstone resurrected: %v %d", err, len(rows))
	}
	var deleted bool
	if err = s.DB().QueryRow("SELECT deleted FROM slack_messages WHERE channel_id='C1'").Scan(&deleted); err != nil || !deleted {
		t.Fatalf("missing terminal tombstone: %v", err)
	}
}

func TestSlackPageAndCheckpointRollbackTogether(t *testing.T) {
	s, id := canonicalStore(t)
	if _, err := s.DB().Exec("CREATE TRIGGER fail_checkpoint BEFORE INSERT ON slack_scans BEGIN SELECT RAISE(ABORT,'injected crash boundary'); END"); err != nil {
		t.Fatal(err)
	}
	page := rawRows(`{"ts":"1700000000.000100","text":"committed only with cursor"}`)
	if err := s.CommitSlackPage(t.Context(), id, "C1", "/conversations.history", "owned", page, SlackScan{Cursor: "next"}); err == nil {
		t.Fatal("injection did not fail")
	}
	rows, _ := s.QuerySlackMessages(t.Context(), SlackQuery{})
	scan, _ := s.SlackScan("owned")
	if len(rows) != 0 || scan.Cursor != "" {
		t.Fatal("page/checkpoint crash left partial commit")
	}
	if _, err := s.DB().Exec("DROP TRIGGER fail_checkpoint"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSlackPage(t.Context(), id, "C1", "/conversations.history", "owned", page, SlackScan{Cursor: "next"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.QuerySlackMessages(t.Context(), SlackQuery{})
	scan, _ = s.SlackScan("owned")
	if len(rows) != 1 || scan.Cursor != "next" {
		t.Fatal("replay did not commit atomically")
	}
	bad := append(page, json.RawMessage(`{"text":"no timestamp"}`))
	if err := s.CommitSlackPage(t.Context(), id, "C1", "/conversations.history", "owned", bad, SlackScan{Complete: true}); err == nil {
		t.Fatal("malformed page accepted")
	}
	scan, _ = s.SlackScan("owned")
	if scan.Complete || scan.Cursor != "next" {
		t.Fatal("invalid page advanced checkpoint")
	}
}

func legacyMirror(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	tables := []string{"slack_mirror_identity", "slack_legacy_rows", "slack_messages", "slack_messages_fts", "slack_scans", "slack_directories", "slack_archive_members"}
	for _, table := range tables {
		if _, err = db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { writer.Close() })
	if _, err = writer.Exec("INSERT INTO resources(resource_type,id,data) VALUES('conversations-conversations-history','1700000000.000100','{\"ts\":\"1700000000.000100\",\"text\":\"legacy in WAL\"}')"); err != nil {
		t.Fatal(err)
	}
	return path, writer
}

func TestSlackLegacyBackupRestoresWALAndConcurrentRecoveryIsIdempotent(t *testing.T) {
	path, writer := legacyMirror(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, e := Open(path)
			if e == nil {
				s.Close()
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	backup, err := sql.Open("sqlite", "file:"+path+".pre-slack-mirror/data.db?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var n, version int
	if err = backup.QueryRow("SELECT count(*) FROM resources WHERE " + legacyMessagesSQL).Scan(&n); err != nil || n != 1 {
		t.Fatalf("WAL snapshot missing committed legacy: %d %v", n, err)
	}
	_ = backup.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 4 {
		t.Fatalf("backup schema %d", version)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(); v != 5 {
		t.Fatalf("migration not versioned: %d", v)
	}
	if err = s.DB().QueryRow("SELECT count(*) FROM slack_legacy_rows").Scan(&n); err != nil || n != 1 {
		t.Fatal("quarantine not idempotent")
	}
	_ = writer.QueryRow("SELECT count(*) FROM resources WHERE " + legacyMessagesSQL).Scan(&n)
	if n != 1 {
		t.Fatal("original legacy evidence removed")
	}
}

func TestSlackLegacyReadOnlyAndFailedBackupDoNotMigrate(t *testing.T) {
	path, writer := legacyMirror(t)
	s, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var version int
	_ = writer.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 4 {
		t.Fatal("read-only migrated")
	}
	if _, err = os.Stat(path + ".pre-slack-mirror"); !os.IsNotExist(err) {
		t.Fatal("read-only created backup")
	}
	if err = os.Mkdir(path+".pre-slack-mirror", 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path+".pre-slack-mirror", "provenance.json"), []byte(`{"Source":"another database","Migration":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err == nil {
		s.Close()
		t.Fatal("unrelated backup adopted")
	}
	_ = writer.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 4 {
		t.Fatal("failed backup migrated schema")
	}
	if err = os.RemoveAll(path + ".pre-slack-mirror"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err = OpenWithContext(ctx, path); err == nil {
		s.Close()
		t.Fatal("cancelled recovery completed")
	}
	if s, err = Open(path); err != nil {
		t.Fatalf("recovery after cancellation: %v", err)
	} else {
		s.Close()
	}
}

func TestSlackLegacyMigrationRollbackReusesVerifiedBackup(t *testing.T) {
	path, writer := legacyMirror(t)
	if _, err := writer.Exec("CREATE TABLE slack_messages(workspace_id TEXT)"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("malformed prestate did not stop migration")
	}
	var version int
	_ = writer.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 4 {
		t.Fatal("failed migration stamped version")
	}
	if _, err := os.Stat(filepath.Join(path+".pre-slack-mirror", "data.db")); err != nil {
		t.Fatal("verified backup not retained")
	}
	if _, err := writer.Exec("DROP TABLE slack_messages"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
	s.Close()
}

func TestSlackCorruptBackupStopsBeforeMigration(t *testing.T) {
	path, writer := legacyMirror(t)
	if _, err := writer.Exec("CREATE TABLE slack_messages(workspace_id TEXT)"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("expected migration rollback")
	}
	if _, err := writer.Exec("DROP TABLE slack_messages"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(path+".pre-slack-mirror", "data.db")
	if err := os.WriteFile(snapshot, []byte("unrelated but nonzero content"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("corrupt backup adopted")
	}
	var v int
	if err := writer.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 4 {
		t.Fatalf("failed backup changed schema %d %v", v, err)
	}
}

func TestSlackFutureSchemaRejectedForReadOnlyAndWritableOpen(t *testing.T) {
	s, _ := canonicalStore(t)
	path := s.path
	if _, err := s.DB().Exec("PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, open := range []func(string) (*Store, error){OpenReadOnly, Open} {
		if db, err := open(path); err == nil {
			db.Close()
			t.Fatal("accepted newer schema")
		}
	}
}
