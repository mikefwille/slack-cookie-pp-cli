// Hand-authored extension (survives regen): shared helpers for the offline,
// store-only commands (digest, missed, stale-threads, trends, sql). They read
// the per-account SQLite mirror read-only and never make live API calls, so
// repeated queries are cheap and rate-limit-safe (KTD6).
//
// Message identity is observing workspace, conversation and exact timestamp.
// Reports use only verified canonical rows; ambiguous legacy rows remain
// recoverable but cannot contribute to report results.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/cliutil"
	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/slackmirror"
	"slack-cookie-pp-cli/internal/store"
)

// newClientForAccount builds a client bound to a specific account name (rather
// than flags.account), so per-account fan-out (missed/stale-threads under
// --all-accounts) can resolve each workspace's own identity.
func newClientForAccount(flags *rootFlags, account string) (*client.Client, error) {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, configErr(err)
	}
	config.ApplyAccount(cfg, account)
	c := client.New(cfg, flags.timeout, flags.rateLimit)
	c.DryRun = flags.dryRun
	c.NoCache = flags.noCache
	return c, nil
}

// resolveAccountUserID returns the Slack user id to match "me" against for a
// given account: the explicit --me flag (the user's override, applied as-is),
// else a single per-account live auth.test (skipped under --data-source local),
// else "". Resolving per account is what makes mention matching correct under
// --all-accounts — separate-login workspaces have distinct user ids, so a single
// shared id silently drops the other accounts' mentions.
func resolveAccountUserID(ctx context.Context, flags *rootFlags, account, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	if s, err := store.OpenReadOnly(accountDBPath(account)); err == nil {
		id, e := s.SlackIdentity()
		s.Close()
		if e == nil && id.UserID != "" {
			return id.UserID
		}
	}
	if flags.dataSource == "local" {
		return ""
	}
	c, err := newClientForAccount(flags, account)
	if err != nil {
		return ""
	}
	raw, err := c.GetNoCache(ctx, "/auth.test", nil)
	if err != nil {
		return ""
	}
	var b struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(raw, &b) == nil && b.OK {
		return b.UserID
	}
	return ""
}

// storedMessage is a parsed message row from the local mirror.
type storedMessage struct {
	Account     string    `json:"account"`
	WorkspaceID string    `json:"workspace_id"`
	Channel     string    `json:"channel,omitempty"`
	TS          string    `json:"ts"`
	Time        time.Time `json:"time"`
	User        string    `json:"user,omitempty"`
	Text        string    `json:"text,omitempty"`
	ThreadTS    string    `json:"thread_ts,omitempty"`
	ReplyCount  int       `json:"reply_count,omitempty"`
	Subtype     string    `json:"subtype,omitempty"`
}

// accountMirror is an opened per-account store, or a missing-mirror marker.
type accountMirror struct {
	Account string
	Path    string
	Store   *store.Store
	Missing bool
}

// openAccountMirrors opens the per-account DB (read-only) for each targeted
// account. A missing DB file is reported as Missing rather than an error so one
// absent mirror never fails the others. Callers must defer closeMirrors.
func openAccountMirrors(ctx context.Context, flags *rootFlags, allAccounts bool) ([]accountMirror, error) {
	targets := resolveAccountSet(flags.configPath, flags.account, allAccounts)
	mirrors := make([]accountMirror, 0, len(targets))
	for _, name := range targets {
		path := accountDBPath(name)
		if path == "" {
			path = defaultDBPath("slack-cookie-pp-cli")
		}
		if _, err := os.Stat(path); err != nil {
			mirrors = append(mirrors, accountMirror{Account: name, Path: path, Missing: true})
			continue
		}
		s, err := store.OpenReadOnlyContext(ctx, path)
		if err != nil {
			mirrors = append(mirrors, accountMirror{Account: name, Path: path, Missing: true})
			continue
		}
		mirrors = append(mirrors, accountMirror{Account: name, Path: path, Store: s})
	}
	return mirrors, nil
}

func closeMirrors(mirrors []accountMirror) {
	for _, m := range mirrors {
		if m.Store != nil {
			_ = m.Store.Close()
		}
	}
}

// missingMirrorHint is the standard guidance when an account has no local DB.
func missingMirrorHint(account string) string {
	return fmt.Sprintf("no local mirror for account %q — run a read command (e.g. conversations history) or sync to populate it", account)
}

// loadMessages returns the message rows in a mirror newer than `since` (a zero
// `since` returns all). It scans every message resource type.
func loadMessages(ctx context.Context, s *store.Store, account string, since time.Time) ([]storedMessage, error) {
	params := map[string]string{}
	if !since.IsZero() {
		params["oldest"] = slackmirror.Stamp(since)
		params["inclusive"] = "true"
	}
	if _, err := s.SlackIdentity(); err != nil {
		return []storedMessage{}, nil
	}
	result, err := slackmirror.Local(ctx, s, "/local.messages", params, true)
	if err != nil {
		return nil, err
	}
	out := make([]storedMessage, 0, len(result.Items))
	for _, raw := range result.Items {
		m, ok := parseStoredMessage(raw, account)
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func parseStoredMessage(raw []byte, account string) (storedMessage, bool) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return storedMessage{}, false
	}
	ts := asString(obj["ts"])
	if ts == "" {
		return storedMessage{}, false
	}
	m := storedMessage{
		Account:     account,
		WorkspaceID: asString(obj["workspace_id"]),
		TS:          ts,
		Time:        tsToTime(ts),
		User:        asString(obj["user"]),
		Text:        asString(obj["text"]),
		ThreadTS:    asString(obj["thread_ts"]),
		Subtype:     asString(obj["subtype"]),
		Channel:     extractMessageChannel(obj),
	}
	if rc, ok := obj["reply_count"].(float64); ok {
		m.ReplyCount = int(rc)
	}
	return m, true
}

// extractMessageChannel pulls the channel id from the shapes Slack uses:
// a bare "channel":"C123" string, or a "channel":{"id":...} object (search
// results). Returns "" when absent (common for history rows).
func extractMessageChannel(obj map[string]any) string {
	switch c := obj["channel"].(type) {
	case string:
		return c
	case map[string]any:
		return asString(c["id"])
	}
	return ""
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		return ""
	}
}

// tsToTime parses a Slack message ts ("1700000000.000100") into a time.Time.
// A malformed ts yields the zero time.
func tsToTime(ts string) time.Time {
	return store.SlackTime(ts)
}

// parseSinceCutoff converts a loose duration ("1d", "4h", "1w", "30m") into an
// absolute cutoff relative to now. An empty value returns the zero time (no
// lower bound).
func parseSinceCutoff(since string) (time.Time, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return time.Time{}, nil
	}
	d, err := cliutil.ParseDurationLoose(since)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q: %w (try 1d, 4h, 1w, 30m)", since, err)
	}
	return time.Now().Add(-d).UTC(), nil
}
