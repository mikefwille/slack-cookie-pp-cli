package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Store) QuerySlackMessages(ctx context.Context, q SlackQuery) ([]json.RawMessage, error) {
	id, err := s.SlackIdentity()
	if err != nil {
		return nil, err
	}
	for _, ts := range []string{q.Oldest, q.Latest, q.Thread, q.AfterTS} {
		if ts != "" && !ValidSlackTS(ts) {
			return nil, fmt.Errorf("invalid Slack timestamp %q", ts)
		}
	}
	args := []any{id.WorkspaceID}
	where := "m.workspace_id=? AND m.deleted=0"
	if q.Channel != "" {
		where += " AND m.channel_id=?"
		args = append(args, q.Channel)
	}
	if q.Thread != "" {
		where += " AND (m.thread_ts=? OR m.ts=?)"
		args = append(args, q.Thread, q.Thread)
	}
	if q.Endpoint == "/conversations.history" {
		where += " AND (m.thread_ts='' OR m.thread_ts=m.ts OR m.history_seen=1)"
	}
	if q.Text != "" {
		where += " AND EXISTS(SELECT 1 FROM slack_messages_fts f WHERE f.workspace_id=m.workspace_id AND f.channel_id=m.channel_id AND f.ts=m.ts AND slack_messages_fts MATCH ?)"
		args = append(args, q.Text)
	}
	for _, bound := range []struct{ value, op string }{{q.Oldest, ">"}, {q.Latest, "<"}} {
		if bound.value == "" {
			continue
		}
		op := bound.op
		if q.Inclusive {
			op += "="
		}
		where += " AND (length(m.ts),m.ts) " + op + " (length(?),?)"
		args = append(args, bound.value, bound.value)
	}
	order := "DESC"
	op := "<"
	if q.Endpoint == "/conversations.replies" {
		order = "ASC"
		op = ">"
	}
	if q.AfterTS != "" {
		where += " AND (length(m.ts),m.ts,m.channel_id) " + op + " (length(?),?,?)"
		args = append(args, q.AfterTS, q.AfterTS, q.AfterChannel)
	}
	query := "SELECT m.data FROM slack_messages m WHERE " + where + " ORDER BY length(m.ts) " + order + ",m.ts " + order + ",m.channel_id " + order
	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *Store) QuerySlackDirectory(ctx context.Context, path string, p map[string]string, after string, limit int) ([]json.RawMessage, error) {
	id, err := s.SlackIdentity()
	if err != nil {
		return nil, err
	}
	rt := "conversations"
	if path == "/users.list" {
		rt = "users"
	}
	where := "workspace_id=? AND user_id=? AND resource_type=?"
	args := []any{id.WorkspaceID, id.UserID, rt}
	if after != "" {
		where += " AND id>?"
		args = append(args, after)
	}
	if path == "/conversations.list" {
		if p["exclude_archived"] == "true" {
			where += " AND coalesce(json_extract(data,'$.is_archived'),0)=0"
		}
		if types := p["types"]; types != "" {
			parts := strings.Split(types, ",")
			marks := make([]string, len(parts))
			for i, part := range parts {
				marks[i] = "?"
				args = append(args, strings.TrimSpace(part))
			}
			where += " AND CASE WHEN json_extract(data,'$.is_im') THEN 'im' WHEN json_extract(data,'$.is_mpim') THEN 'mpim' WHEN json_extract(data,'$.is_private') THEN 'private_channel' ELSE 'public_channel' END IN (" + strings.Join(marks, ",") + ")"
		}
	}
	query := "SELECT data FROM slack_directories WHERE " + where + " ORDER BY id ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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
