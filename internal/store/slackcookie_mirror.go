// Hand-authored Slack mirror: verified identity, canonical messages and scan progress.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type SlackIdentity struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id,omitempty"`
}

type SlackCoverage struct {
	State             string   `json:"state"`
	Reasons           []string `json:"reasons,omitempty"`
	LegacyRows        int      `json:"excluded_legacy_rows,omitempty"`
	Deletions         string   `json:"deletion_coverage"`
	Oldest            string   `json:"oldest,omitempty"`
	Latest            string   `json:"latest,omitempty"`
	TraversalComplete bool     `json:"traversal_complete"`
}

type SlackScan struct {
	Cursor           string `json:"cursor"`
	Complete         bool   `json:"complete"`
	Reason           string `json:"reason,omitempty"`
	RetentionLimited bool   `json:"retention_limited,omitempty"`
}

var slackTS = regexp.MustCompile(`^(0|[1-9][0-9]*)\.[0-9]{6}$`)

func ValidSlackTS(s string) bool { return slackTS.MatchString(s) }

func NormalizeSlackBound(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	parts := strings.SplitN(s, ".", 2)
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 6 {
		return "", fmt.Errorf("Slack timestamp bounds support at most six decimal places")
	}
	normalized := parts[0] + "." + fraction + strings.Repeat("0", 6-len(fraction))
	if !ValidSlackTS(normalized) {
		return "", fmt.Errorf("invalid Slack timestamp bound %q", s)
	}
	return normalized, nil
}

// CompareSlackTS compares exact decimal timestamp strings, never binary floats.
func CompareSlackTS(a, b string) int {
	aa, bb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	if len(aa[0]) != len(bb[0]) {
		if len(aa[0]) < len(bb[0]) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func SlackTime(ts string) time.Time {
	if !ValidSlackTS(ts) {
		return time.Time{}
	}
	p := strings.SplitN(ts, ".", 2)
	sec, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil {
		return time.Time{}
	}
	micro, _ := strconv.ParseInt(p[1], 10, 64)
	return time.Unix(sec, micro*1000).UTC()
}

const legacyMessagesSQL = `resource_type IN ('conversations-conversations-history','conversations-conversations-replies','messages')`

func (s *Store) migrateSlack(ctx context.Context, conn *sql.Conn) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS slack_mirror_identity (singleton INTEGER PRIMARY KEY CHECK(singleton=1), workspace_id TEXT NOT NULL, user_id TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS slack_legacy_rows (resource_type TEXT NOT NULL,id TEXT NOT NULL,data TEXT NOT NULL,reason TEXT NOT NULL,PRIMARY KEY(resource_type,id))`,
		`CREATE TABLE IF NOT EXISTS slack_messages (workspace_id TEXT NOT NULL,channel_id TEXT NOT NULL,ts TEXT NOT NULL,thread_ts TEXT NOT NULL,text TEXT NOT NULL,revision_ts TEXT NOT NULL,deleted INTEGER NOT NULL,priority INTEGER NOT NULL,history_seen INTEGER NOT NULL,sources TEXT NOT NULL,observed_at TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(workspace_id,channel_id,ts))`,
		`CREATE INDEX IF NOT EXISTS slack_messages_thread ON slack_messages(workspace_id,channel_id,thread_ts,ts)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS slack_messages_fts USING fts5(workspace_id UNINDEXED,channel_id UNINDEXED,ts UNINDEXED,text,tokenize='porter unicode61')`,
		`CREATE TABLE IF NOT EXISTS slack_scans (scope TEXT PRIMARY KEY,cursor TEXT NOT NULL,complete INTEGER NOT NULL,reason TEXT NOT NULL,retention_limited INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS slack_directories (workspace_id TEXT NOT NULL,user_id TEXT NOT NULL,resource_type TEXT NOT NULL,id TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(workspace_id,user_id,resource_type,id))`,
		`CREATE TABLE IF NOT EXISTS slack_archive_members (scope TEXT NOT NULL,id TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(scope,id))`,
		`CREATE INDEX IF NOT EXISTS slack_message_order ON slack_messages(workspace_id,length(ts),ts,channel_id)`,
		"INSERT OR IGNORE INTO slack_legacy_rows SELECT resource_type,id,data,'identity_unverified; re-fetch with sync --resources conversations,messages' FROM resources WHERE " + legacyMessagesSQL,
	}
	for _, q := range statements {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SlackIdentity() (SlackIdentity, error) {
	var id SlackIdentity
	err := s.db.QueryRow("SELECT workspace_id,user_id FROM slack_mirror_identity WHERE singleton=1").Scan(&id.WorkspaceID, &id.UserID)
	if err != nil {
		return id, fmt.Errorf("mirror identity unavailable; re-fetch with sync --resources conversations,messages: %w", err)
	}
	return id, nil
}

func (s *Store) BindSlackIdentity(id SlackIdentity) error {
	if id.WorkspaceID == "" || id.UserID == "" {
		return fmt.Errorf("verified Slack workspace and observing user identity are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspace, user string
	err = tx.QueryRow("SELECT workspace_id,user_id FROM slack_mirror_identity WHERE singleton=1").Scan(&workspace, &user)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if workspace != "" && workspace != id.WorkspaceID {
		return fmt.Errorf("workspace identity mismatch: mirror is bound to %s, authenticated workspace is %s; use a separate mirror", workspace, id.WorkspaceID)
	}
	if user != "" && user != id.UserID {
		return fmt.Errorf("observing user identity mismatch: use a separate mirror for another Slack observer")
	}
	if id.UserID == "" {
		id.UserID = user
	}
	if _, err = tx.Exec("INSERT INTO slack_mirror_identity VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET user_id=excluded.user_id", id.WorkspaceID, id.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SlackScan(scope string) (SlackScan, error) {
	var p SlackScan
	err := s.db.QueryRow("SELECT cursor,complete,reason,retention_limited FROM slack_scans WHERE scope=?", scope).Scan(&p.Cursor, &p.Complete, &p.Reason, &p.RetentionLimited)
	if err == sql.ErrNoRows {
		return p, nil
	}
	return p, err
}

func (s *Store) SaveSlackScan(scope string, p SlackScan) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec("INSERT INTO slack_scans VALUES(?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET cursor=excluded.cursor,complete=excluded.complete,reason=excluded.reason,retention_limited=excluded.retention_limited", scope, p.Cursor, p.Complete, p.Reason, p.RetentionLimited)
	return err
}

func (s *Store) SlackCoverage(scope string) SlackCoverage {
	c := SlackCoverage{State: "partial", Reasons: []string{"source_not_fully_enumerated"}, Deletions: "unavailable"}
	if _, err := s.SlackIdentity(); err != nil {
		c.State = "unavailable"
		c.Reasons = []string{"workspace_identity_unavailable"}
	}
	p, err := s.SlackScan(scope)
	if err == nil && p.Complete && !p.RetentionLimited && c.State != "unavailable" {
		c.State = "complete"
		c.Reasons = nil
		c.TraversalComplete = true
	}
	if p.RetentionLimited {
		c.Reasons = append(c.Reasons, "retention_limited")
	}
	if p.Reason != "" {
		c.Reasons = append(c.Reasons, p.Reason)
		if c.State == "complete" {
			c.State = "partial"
		}
	}
	_ = s.db.QueryRow("SELECT count(*) FROM resources WHERE " + legacyMessagesSQL).Scan(&c.LegacyRows)
	if c.LegacyRows > 0 {
		c.Reasons = append(c.Reasons, "legacy_identity_unverified")
	}
	return c
}

func slackString(m map[string]any, k string) string { v, _ := m[k].(string); return v }

// CommitSlackPage validates and commits observations and checkpoint together.
// A malformed row rolls back the whole page and cannot advance its cursor.
func (s *Store) CommitSlackPage(ctx context.Context, id SlackIdentity, channel, source, scope string, items []json.RawMessage, scan SlackScan) error {
	current, err := s.SlackIdentity()
	if err != nil {
		return err
	}
	if current.WorkspaceID != id.WorkspaceID || current.UserID != id.UserID || id.WorkspaceID == "" || id.UserID == "" {
		return fmt.Errorf("workspace identity mismatch")
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
		if obj == nil {
			return fmt.Errorf("invalid Slack message object")
		}
		subtype := slackString(obj, "subtype")
		deletion := subtype == "message_deleted"
		eventRevision := slackString(obj, "event_ts")
		if subtype == "message_changed" {
			nested, ok := obj["message"].(map[string]any)
			if !ok {
				return fmt.Errorf("message_changed lacks message")
			}
			obj = nested
		}
		ts := slackString(obj, "ts")
		if deletion {
			ts = slackString(obj, "deleted_ts")
		}
		if !ValidSlackTS(ts) {
			return fmt.Errorf("invalid Slack message timestamp %q", ts)
		}
		ch := channel
		explicit := ""
		switch v := obj["channel"].(type) {
		case string:
			explicit = v
		case map[string]any:
			explicit = slackString(v, "id")
		}
		if explicit != "" && ch != "" && explicit != ch {
			return fmt.Errorf("message conversation identity conflicts with request")
		}
		if ch == "" {
			ch = explicit
		}
		if ch == "" {
			return fmt.Errorf("message conversation identity is missing")
		}
		thread := slackString(obj, "thread_ts")
		if thread != "" && !ValidSlackTS(thread) {
			return fmt.Errorf("invalid thread timestamp")
		}
		revision := ts
		if edited, ok := obj["edited"].(map[string]any); ok {
			r := slackString(edited, "ts")
			if !ValidSlackTS(r) {
				return fmt.Errorf("invalid edited timestamp")
			}
			revision = r
		}
		if ValidSlackTS(eventRevision) && CompareSlackTS(eventRevision, revision) > 0 {
			revision = eventRevision
		}
		priority := 1
		history := false
		switch source {
		case "/conversations.history":
			priority = 3
			history = true
		case "/conversations.replies":
			priority = 4
		}
		var oldRaw, oldRevision, oldSources string
		var oldDeleted, oldHistory bool
		var oldPrio int
		err = tx.QueryRowContext(ctx, "SELECT data,revision_ts,deleted,priority,history_seen,sources FROM slack_messages WHERE workspace_id=? AND channel_id=? AND ts=?", id.WorkspaceID, ch, ts).Scan(&oldRaw, &oldRevision, &oldDeleted, &oldPrio, &oldHistory, &oldSources)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		sources := map[string]bool{}
		_ = json.Unmarshal([]byte(oldSources), &sources)
		sources[source] = true
		if err == nil {
			var previous map[string]any
			_ = json.Unmarshal([]byte(oldRaw), &previous)
			if oldDeleted {
				obj = previous
				revision = oldRevision
				deletion = true
				priority = oldPrio
			} else if !deletion {
				incoming := obj
				authoritative := source == "/conversations.history" || source == "/conversations.replies"
				newContent := CompareSlackTS(revision, oldRevision) > 0 || (revision == oldRevision && (authoritative || oldPrio == 1))
				if !newContent {
					obj = previous
					revision = oldRevision
					priority = oldPrio
				} else if !authoritative && oldPrio > 1 {
					// Search can describe an edit, but is not a full authoritative
					// object. Apply its explicit content fields without removing
					// authoritative fields absent from its abbreviated shape.
					obj = previous
					for _, key := range []string{"text", "edited", "blocks", "attachments", "files"} {
						if value, ok := incoming[key]; ok {
							obj[key] = value
						}
					}
					priority = oldPrio
				}
				// Thread metadata is observed independently of content edits.
				// Preserve newer known thread activity when an older copy replays.
				oldLatest := slackString(previous, "latest_reply")
				newLatest := slackString(incoming, "latest_reply")
				metadataCurrent := authoritative && (oldLatest == "" || (newLatest != "" && CompareSlackTS(newLatest, oldLatest) >= 0))
				for _, key := range []string{"thread_ts", "reply_count", "reply_users", "reply_users_count", "latest_reply", "subscribed", "last_read"} {
					if metadataCurrent {
						if value, ok := incoming[key]; ok {
							obj[key] = value
						} else if value, ok := previous[key]; ok {
							obj[key] = value
						}
					} else if value, ok := previous[key]; ok {
						obj[key] = value
					}
				}
			}
		}
		obj["ts"] = ts
		obj["channel"] = ch
		obj["workspace_id"] = id.WorkspaceID
		if deletion {
			obj = map[string]any{"ts": ts, "channel": ch, "workspace_id": id.WorkspaceID, "subtype": "message_deleted"}
		}
		thread = slackString(obj, "thread_ts")
		text := slackString(obj, "text")
		encoded, _ := json.Marshal(obj)
		sourceJSON, _ := json.Marshal(sources)
		_, err = tx.ExecContext(ctx, `INSERT INTO slack_messages VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,channel_id,ts) DO UPDATE SET thread_ts=excluded.thread_ts,text=excluded.text,revision_ts=excluded.revision_ts,deleted=excluded.deleted,priority=excluded.priority,history_seen=excluded.history_seen,sources=excluded.sources,observed_at=excluded.observed_at,data=excluded.data`, id.WorkspaceID, ch, ts, thread, text, revision, deletion, priority, history || oldHistory, string(sourceJSON), time.Now().UTC().Format(time.RFC3339Nano), string(encoded))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM slack_messages_fts WHERE workspace_id=? AND channel_id=? AND ts=?", id.WorkspaceID, ch, ts); err != nil {
			return err
		}
		if !deletion {
			if _, err = tx.ExecContext(ctx, "INSERT INTO slack_messages_fts(workspace_id,channel_id,ts,text) VALUES(?,?,?,?)", id.WorkspaceID, ch, ts, text); err != nil {
				return err
			}
		}
	}
	if scope != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO slack_scans VALUES(?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET cursor=excluded.cursor,complete=excluded.complete,reason=excluded.reason,retention_limited=excluded.retention_limited", scope, scan.Cursor, scan.Complete, scan.Reason, scan.RetentionLimited); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type SlackQuery struct {
	Channel, Thread, Oldest, Latest, Text, Endpoint string
	Inclusive                                       bool
	AfterTS, AfterChannel                           string
	Limit                                           int
}
