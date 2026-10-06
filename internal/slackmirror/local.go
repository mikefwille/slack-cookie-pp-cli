package slackmirror

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slack-cookie-pp-cli/internal/store"
	"strconv"
	"strings"
)

// CoverageFor requires a completed certificate that contains this exact
// conversation/thread/range. Quarantine warnings survive successful re-fetch
// without redefining a completed source traversal as incomplete.
func CoverageFor(s *store.Store, id store.SlackIdentity, path string, p map[string]string) store.SlackCoverage {
	base := s.SlackCoverage("missing-certificate")
	target := path
	if path == "/local.search" || path == "/local.messages" {
		target = "/archive"
	}
	rows, err := s.DB().Query("SELECT scope,complete,reason,retention_limited FROM slack_scans")
	if err != nil {
		return base
	}
	defer rows.Close()
	for rows.Next() {
		var raw, reason string
		var complete, limited bool
		if rows.Scan(&raw, &complete, &reason, &limited) != nil || !complete || limited || reason != "" {
			continue
		}
		prefix, payload, ok := strings.Cut(raw, "|")
		if !ok || (prefix != "archive" && prefix != "observed") {
			continue
		}
		var certificate struct {
			Workspace, Observer, Endpoint string
			Params                        map[string]string
		}
		if json.Unmarshal([]byte(payload), &certificate) != nil || certificate.Workspace != id.WorkspaceID || certificate.Observer != id.UserID || certificate.Endpoint != target {
			continue
		}
		q := certificate.Params
		if q["initial_cursor"] != "" {
			continue
		}
		if target != "/archive" {
			if q["channel"] != p["channel"] || q["ts"] != p["ts"] {
				continue
			}
			if target == "/conversations.list" && (q["types"] != p["types"] || q["exclude_archived"] != p["exclude_archived"]) {
				continue
			}
		} else {
			var discovery struct {
				Endpoint string
				Params   map[string]string
			}
			if json.Unmarshal([]byte(q["discovery"]), &discovery) != nil {
				continue
			}
			if p["channel"] == "" {
				if discovery.Params["exclude_archived"] != "false" || !allConversationTypes(discovery.Params["types"]) {
					continue
				}
			} else {
				var member int
				if s.DB().QueryRow("SELECT count(*) FROM slack_archive_members WHERE scope=? AND id=?", "archive|"+q["discovery"], p["channel"]).Scan(&member) != nil || member == 0 {
					continue
				}
			}
		}
		if !rangeContains(q, p) {
			continue
		}
		base.State = "complete"
		base.TraversalComplete = true
		base.Reasons = nil
		base.Oldest = q["oldest"]
		base.Latest = q["latest"]
		if base.LegacyRows > 0 {
			base.Reasons = append(base.Reasons, "legacy_identity_unverified")
		}
		return base
	}
	return base
}

func allConversationTypes(types string) bool {
	for _, typ := range []string{"public_channel", "private_channel", "im", "mpim"} {
		if !containsType(types, typ) {
			return false
		}
	}
	return true
}

func rangeContains(candidate, query map[string]string) bool {
	for _, key := range []string{"oldest", "latest"} {
		c, q := candidate[key], query[key]
		if c == "" {
			continue
		}
		if q == "" {
			return false
		}
		cmp := store.CompareSlackTS(c, q)
		if key == "oldest" && cmp > 0 {
			return false
		}
		if key == "latest" && cmp < 0 {
			return false
		}
		if cmp == 0 && query["inclusive"] == "true" && candidate["inclusive"] != "true" {
			return false
		}
	}
	return true
}

func Local(ctx context.Context, s *store.Store, path string, params map[string]string, all bool) (Result, error) {
	p := Clone(params)
	if err := validate(path, p); err != nil {
		return Result{}, err
	}
	allowed := map[string]bool{"channel": true, "ts": true, "oldest": true, "latest": true, "inclusive": true, "limit": true, "cursor": true, "query": true, "types": true, "exclude_archived": true}
	for key := range p {
		if !allowed[key] {
			return Result{}, fmt.Errorf("unsupported local filter %q", key)
		}
	}
	id, err := s.SlackIdentity()
	if err != nil {
		return Result{}, err
	}
	hash := scopeHash(id, path, p)
	key := localCursor{}
	if cursor := p["cursor"]; cursor != "" {
		if !strings.HasPrefix(cursor, "local:v1:") {
			return Result{}, fmt.Errorf("local mode requires a local continuation token, not a Slack cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "local:v1:"))
		if e != nil {
			return Result{}, fmt.Errorf("invalid local continuation token")
		}
		if e = json.Unmarshal(b, &key); e != nil || key.Scope != hash {
			return Result{}, fmt.Errorf("local continuation token belongs to another workspace, query or order")
		}
		if key.TS != "" && !store.ValidSlackTS(key.TS) {
			return Result{}, fmt.Errorf("invalid local continuation timestamp")
		}
	}
	limit := 100
	if p["limit"] != "" {
		limit, _ = strconv.Atoi(p["limit"])
	}
	if limit < 1 || limit == int(^uint(0)>>1) {
		return Result{}, fmt.Errorf("invalid local page limit")
	}
	sqlLimit := limit + 1
	if all {
		sqlLimit = 0
	}
	var items []json.RawMessage
	switch path {
	case "/conversations.history", "/conversations.replies", "/local.search", "/local.messages":
		if path == "/local.search" && strings.Contains(p["query"], ":") {
			return Result{}, fmt.Errorf("Slack search qualifiers are unsupported locally; use --channel, --oldest, --latest and --thread-ts")
		}
		if p["cursor"] != "" && key.TS == "" {
			return Result{}, fmt.Errorf("invalid local message continuation")
		}
		items, err = s.QuerySlackMessages(ctx, store.SlackQuery{Channel: p["channel"], Thread: p["ts"], Oldest: p["oldest"], Latest: p["latest"], Text: p["query"], Endpoint: path, Inclusive: p["inclusive"] == "true", AfterTS: key.TS, AfterChannel: key.Channel, Limit: sqlLimit})
	case "/conversations.list", "/users.list":
		if p["cursor"] != "" && key.ID == "" {
			return Result{}, fmt.Errorf("invalid local directory continuation")
		}
		items, err = s.QuerySlackDirectory(ctx, path, p, key.ID, sqlLimit)
	default:
		return Result{}, fmt.Errorf("unsupported local Slack collection")
	}
	if err != nil {
		return Result{}, err
	}
	out := Result{Items: items, Coverage: CoverageFor(s, id, path, p), Source: "local", WorkspaceID: id.WorkspaceID}
	if !all && len(items) > limit {
		out.Items = items[:limit]
		out.HasMore = true
		out.NextCursor = nextLocal(hash, out.Items[len(out.Items)-1])
	}
	return out, nil
}
