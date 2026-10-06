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

type slackBackupPlan struct {
	Expected slackBackupProvenance
	Bundle   string
}

// Preflight and publication are separate because another opener may complete
// migration between them. The original source/schema remain the trust anchors.
func (s *Store) planSlackBackup(ctx context.Context) (*slackBackupPlan, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version >= 5 {
		return nil, nil
	}
	var exists, count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='resources'").Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM resources WHERE "+legacyMessagesSQL).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	source, err := filepath.Abs(s.path)
	if err != nil {
		return nil, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	plan := &slackBackupPlan{Expected: slackBackupProvenance{Source: source, FileID: sourceFileID(info), Migration: 5, Schema: version}, Bundle: s.path + ".pre-slack-mirror"}
	if _, err = os.Stat(plan.Bundle); err == nil {
		return nil, plan.validate(ctx, plan.Bundle)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return plan, nil
}

func (p *slackBackupPlan) validate(ctx context.Context, dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "provenance.json"))
	if err != nil {
		return fmt.Errorf("backup provenance unavailable: %w", err)
	}
	var actual slackBackupProvenance
	if err = json.Unmarshal(raw, &actual); err != nil {
		return fmt.Errorf("invalid backup provenance")
	}
	if actual.Source != p.Expected.Source || actual.FileID != p.Expected.FileID || actual.Migration != p.Expected.Migration || actual.Schema != p.Expected.Schema {
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
	if schema != p.Expected.Schema {
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

func (s *Store) snapshotSlackBackup(ctx context.Context, plan *slackBackupPlan, dir string) error {
	snapshot := filepath.Join(dir, "data.db")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(snapshot, "'", "''")+"'"); err != nil {
		return fmt.Errorf("consistent legacy backup: %w", err)
	}
	if err := os.Chmod(snapshot, 0600); err != nil {
		return err
	}
	expected := plan.Expected
	var err error
	expected.SHA256, err = snapshotHash(snapshot)
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(expected)
	return os.WriteFile(filepath.Join(dir, "provenance.json"), metadata, 0600)
}

// Publish the snapshot and its provenance as one atomic directory. A concurrent
// completed migration is safe only when its winning pre-migration bundle passes
// every original provenance, checksum, schema and integrity check.
func (s *Store) publishSlackBackup(ctx context.Context, plan *slackBackupPlan, tmp string) error {
	if err := plan.validate(ctx, tmp); err != nil {
		var version int
		if ctx.Err() != nil || s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version != plan.Expected.Migration {
			return err
		}
		return plan.validate(ctx, plan.Bundle)
	}
	for _, path := range []string{filepath.Join(tmp, "data.db"), filepath.Join(tmp, "provenance.json"), tmp} {
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
	if err := os.Rename(tmp, plan.Bundle); err != nil {
		if _, statErr := os.Stat(plan.Bundle); statErr != nil {
			return err
		}
		if err = plan.validate(ctx, plan.Bundle); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(plan.Bundle))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Original legacy rows remain untouched. Unrelated/corrupt completed bundles
// are refused; interrupted temporary bundles are never adopted.
func (s *Store) prepareSlackBackup(ctx context.Context) error {
	plan, err := s.planSlackBackup(ctx)
	if err != nil || plan == nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(s.path), ".slack-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = s.snapshotSlackBackup(ctx, plan, tmp); err != nil {
		return err
	}
	return s.publishSlackBackup(ctx, plan, tmp)
}
