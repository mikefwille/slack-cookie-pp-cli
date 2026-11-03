package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Store) CommitSlackDirectoryPage(ctx context.Context, id SlackIdentity, resource, scope string, items []json.RawMessage, scan SlackScan) error {
	current, err := s.SlackIdentity()
	if err != nil {
		return err
	}
	if current != id || id.WorkspaceID == "" || id.UserID == "" {
		return fmt.Errorf("Slack observer identity mismatch")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, raw := range items {
		var obj map[string]any
		if err = json.Unmarshal(raw, &obj); err != nil {
			return err
		}
		key, _ := obj["id"].(string)
		if key == "" {
			return fmt.Errorf("directory row has no verified identity")
		}
		if err = s.upsertGenericResourceTx(tx, resource, key, raw); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO slack_directories VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,user_id,resource_type,id) DO UPDATE SET data=excluded.data", id.WorkspaceID, id.UserID, resource, key, string(raw)); err != nil {
			return err
		}
		if resource == "conversations" && strings.HasPrefix(scope, "archive|") {
			if _, err = tx.ExecContext(ctx, "INSERT INTO slack_archive_members VALUES(?,?,?) ON CONFLICT(scope,id) DO UPDATE SET data=excluded.data", scope, key, string(raw)); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO slack_scans VALUES(?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET cursor=excluded.cursor,complete=excluded.complete,reason=excluded.reason,retention_limited=excluded.retention_limited", scope, scan.Cursor, scan.Complete, scan.Reason, scan.RetentionLimited); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SlackArchiveMembers(ctx context.Context, scope string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM slack_archive_members WHERE scope=? ORDER BY id", scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]json.RawMessage, 0)
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}
