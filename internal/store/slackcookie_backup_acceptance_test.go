package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSlackBackupConcurrentUpgradeAfterPreflight(t *testing.T) {
	for _, winner := range []string{"valid", "corrupt", "unrelated"} {
		t.Run(winner, func(t *testing.T) {
			path, writer := legacyMirror(t)
			first := &Store{db: writer, path: path}
			plan, err := first.planSlackBackup(t.Context())
			if err != nil || plan == nil || plan.Expected.Schema != 4 {
				t.Fatalf("preflight: %+v %v", plan, err)
			}
			if _, err = os.Stat(plan.Bundle); !os.IsNotExist(err) {
				t.Fatal("race requires absent bundle at first preflight")
			}
			// The second opener publishes schema 4 and fully migrates before the first
			// opener takes its snapshot. This is forced ordering, independent of timing.
			second, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			second.Close()
			tmp := t.TempDir()
			if err = first.snapshotSlackBackup(t.Context(), plan, tmp); err != nil {
				t.Fatal(err)
			}
			snapshot, err := sql.Open("sqlite", "file:"+filepath.Join(tmp, "data.db")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			var version int
			if err = snapshot.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			snapshot.Close()
			if version != 5 {
				t.Fatalf("first opener must observe migrated snapshot, got %d", version)
			}
			if err = plan.validate(t.Context(), tmp); err == nil {
				t.Fatal("schema 5 snapshot must not masquerade as schema 4 backup")
			}
			if err = plan.validate(t.Context(), plan.Bundle); err != nil {
				t.Fatalf("second opener did not preserve verified schema 4: %v", err)
			}
			originalHash, err := snapshotHash(filepath.Join(plan.Bundle, "data.db"))
			if err != nil {
				t.Fatal(err)
			}
			switch winner {
			case "corrupt":
				if err = os.WriteFile(filepath.Join(plan.Bundle, "data.db"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unrelated":
				metadata := plan.Expected
				metadata.Source = "unrelated database"
				metadata.SHA256 = originalHash
				raw, _ := json.Marshal(metadata)
				if err = os.WriteFile(filepath.Join(plan.Bundle, "provenance.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = first.publishSlackBackup(t.Context(), plan, tmp)
			if winner != "valid" {
				if err == nil {
					t.Fatal("invalid concurrent winner adopted")
				}
				return
			}
			if err != nil {
				t.Fatalf("valid concurrent winner rejected: %v", err)
			}
			if err = first.migrate(t.Context()); err != nil {
				t.Fatalf("first opener did not complete safely: %v", err)
			}
			hash, err := snapshotHash(filepath.Join(plan.Bundle, "data.db"))
			if err != nil || hash != originalHash {
				t.Fatal("first opener replaced pre-migration winner")
			}
			var original, quarantine int
			if err = writer.QueryRow("SELECT count(*) FROM resources WHERE " + legacyMessagesSQL).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if err = writer.QueryRow("SELECT count(*) FROM slack_legacy_rows").Scan(&quarantine); err != nil || original != 1 || quarantine != 1 {
				t.Fatalf("legacy evidence changed: %d %d %v", original, quarantine, err)
			}
		})
	}
}
