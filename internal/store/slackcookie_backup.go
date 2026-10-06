package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type slackBackupProvenance struct {
	Source    string
	FileID    string
	Migration int
	Schema    int
	SHA256    string
}

func sourceFileID(info os.FileInfo) string {
	// Use the OS file identity where supplied. Reflection keeps the generated
	// CLI cross-compilable; Windows and Unix Stat structures have different shapes.
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.IsValid() && v.Kind() == reflect.Struct {
		dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
		if dev.IsValid() && ino.IsValid() {
			return fmt.Sprintf("%v:%v", dev.Interface(), ino.Interface())
		}
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func snapshotHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// The snapshot and its source provenance publish as one atomic directory.
// Unrelated/corrupt completed bundles are refused; interrupted temporary
// bundles are never adopted. Original legacy rows remain untouched.
func (s *Store) prepareSlackBackup(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 5 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='resources'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM resources WHERE "+legacyMessagesSQL).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	source, err := filepath.Abs(s.path)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	expected := slackBackupProvenance{Source: source, FileID: sourceFileID(info), Migration: 5, Schema: version}
	bundle := s.path + ".pre-slack-mirror"
	validate := func(dir string) error {
		raw, err := os.ReadFile(filepath.Join(dir, "provenance.json"))
		if err != nil {
			return fmt.Errorf("backup provenance unavailable: %w", err)
		}
		var actual slackBackupProvenance
		if err = json.Unmarshal(raw, &actual); err != nil {
			return fmt.Errorf("invalid backup provenance")
		}
		if actual.Source != expected.Source || actual.FileID != expected.FileID || actual.Migration != 5 || actual.Schema != version {
			return fmt.Errorf("backup provenance does not match this source/migration")
		}
		path := filepath.Join(dir, "data.db")
		hash, err := snapshotHash(path)
		if err != nil {
			return err
		}
		if hash != actual.SHA256 {
			return fmt.Errorf("backup checksum mismatch")
		}
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			return err
		}
		defer db.Close()
		var result string
		if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("backup integrity check failed")
		}
		var n, schema int
		if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
			return err
		}
		if schema != version {
			return fmt.Errorf("backup schema provenance mismatch")
		}
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM resources WHERE "+legacyMessagesSQL).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("backup contains no legacy messages")
		}
		return nil
	}
	if _, err = os.Stat(bundle); err == nil {
		return validate(bundle)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(s.path), ".slack-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "data.db")
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(snapshot, "'", "''")+"'"); err != nil {
		return fmt.Errorf("consistent legacy backup: %w", err)
	}
	if err = os.Chmod(snapshot, 0600); err != nil {
		return err
	}
	expected.SHA256, err = snapshotHash(snapshot)
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(expected)
	if err = os.WriteFile(filepath.Join(tmp, "provenance.json"), metadata, 0600); err != nil {
		return err
	}
	if err = validate(tmp); err != nil {
		return err
	}
	for _, path := range []string{snapshot, filepath.Join(tmp, "provenance.json"), tmp} {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	if err = os.Rename(tmp, bundle); err != nil {
		if _, statErr := os.Stat(bundle); statErr != nil {
			return err
		}
		if err = validate(bundle); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(bundle))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
