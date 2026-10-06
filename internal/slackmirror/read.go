// Package slackmirror is the shared Slack collection contract for CLI and MCP.
package slackmirror

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/store"
)

type Result struct {
	Items       []json.RawMessage   `json:"results"`
	Coverage    store.SlackCoverage `json:"coverage"`
	NextCursor  string              `json:"next_cursor,omitempty"`
	HasMore     bool                `json:"has_more"`
	Source      string              `json:"source"`
	WorkspaceID string              `json:"workspace_id"`
}

type APIError struct{ Code string }

func (e *APIError) Error() string { return "Slack API rejected collection: " + e.Code }

func Supported(path string) bool {
	switch path {
	case "/conversations.list", "/conversations.history", "/conversations.replies", "/search.messages", "/users.list":
		return true
	}
	return false
}

func Clone(params map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range params {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func Scope(id store.SlackIdentity, path string, params map[string]string) string {
	p := Clone(params)
	for _, key := range []string{"cursor", "limit", "count", "page"} {
		delete(p, key)
	}
	data, _ := json.Marshal(struct {
		Workspace, Observer, Endpoint string
		Params                        map[string]string
	}{id.WorkspaceID, id.UserID, path, p})
	return string(data)
}

func Verify(ctx context.Context, c *client.Client) (store.SlackIdentity, error) {
	raw, err := c.GetNoCache(ctx, "/auth.test", nil)
	if err != nil {
		return store.SlackIdentity{}, err
	}
	var body struct {
		OK     bool   `json:"ok"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
		Error  string `json:"error"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		return store.SlackIdentity{}, fmt.Errorf("verifying observing workspace: invalid auth.test response")
	}
	if !body.OK || body.TeamID == "" || body.UserID == "" {
		return store.SlackIdentity{}, fmt.Errorf("verified observing workspace unavailable (auth.test %s)", body.Error)
	}
	return store.SlackIdentity{WorkspaceID: body.TeamID, UserID: body.UserID}, nil
}

func ArchiveScope(id store.SlackIdentity, path string, p map[string]string) string {
	return "archive|" + Scope(id, path, p)
}
func observationScope(id store.SlackIdentity, path string, p map[string]string) string {
	q := Clone(p)
	if q["cursor"] != "" {
		q["initial_cursor"] = q["cursor"]
	}
	return "observed|" + Scope(id, path, q)
}

type Reader struct {
	Client   *client.Client
	Store    *store.Store
	Identity store.SlackIdentity
}

func Open(ctx context.Context, c *client.Client, path string) (*Reader, error) {
	id, err := Verify(ctx, c)
	if err != nil {
		return nil, err
	}
	s, err := store.OpenWithContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err = s.BindSlackIdentity(id); err != nil {
		s.Close()
		return nil, err
	}
	return &Reader{Client: c, Store: s, Identity: id}, nil
}

func validate(path string, p map[string]string) error {
	if (path == "/conversations.history" || path == "/conversations.replies") && p["channel"] == "" {
		return fmt.Errorf("channel is required")
	}
	if path == "/conversations.replies" && !store.ValidSlackTS(p["ts"]) {
		return fmt.Errorf("exact thread timestamp is required")
	}
	for _, key := range []string{"oldest", "latest"} {
		normalized, err := store.NormalizeSlackBound(p[key])
		if err != nil {
			return err
		}
		if normalized != "" {
			p[key] = normalized
		}
	}
	for _, key := range []string{"limit", "count"} {
		if p[key] != "" {
			v, e := strconv.Atoi(p[key])
			if e != nil || v < 1 {
				return fmt.Errorf("%s must be positive", key)
			}
		}
	}
	if p["oldest"] != "" && p["latest"] != "" && store.CompareSlackTS(p["oldest"], p["latest"]) > 0 {
		return fmt.Errorf("oldest is after latest")
	}
	return nil
}

func Decode(path string, raw json.RawMessage) ([]json.RawMessage, string, bool, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return nil, "", false, fmt.Errorf("invalid Slack collection envelope")
	}
	var ok bool
	if err := json.Unmarshal(envelope["ok"], &ok); err != nil {
		return nil, "", false, fmt.Errorf("Slack collection lacks ok status")
	}
	if !ok {
		var code string
		_ = json.Unmarshal(envelope["error"], &code)
		return nil, "", false, &APIError{Code: code}
	}
	var items []json.RawMessage
	key := "messages"
	switch path {
	case "/conversations.list":
		key = "channels"
	case "/users.list":
		key = "members"
	}
	if path == "/search.messages" {
		var messages struct {
			Matches    []json.RawMessage         `json:"matches"`
			Paging     struct{ Page, Pages int } `json:"paging"`
			Pagination struct {
				Page  int `json:"page"`
				Pages int `json:"page_count"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(envelope["messages"], &messages); err != nil || messages.Matches == nil {
			return nil, "", false, fmt.Errorf("Slack search lacks messages.matches")
		}
		page, pages := messages.Paging.Page, messages.Paging.Pages
		if messages.Pagination.Pages > 0 {
			page, pages = messages.Pagination.Page, messages.Pagination.Pages
		}
		next := ""
		if page < pages {
			next = fmt.Sprintf("page:%d", page+1)
		}
		return messages.Matches, next, next != "", nil
	}
	if err := json.Unmarshal(envelope[key], &items); err != nil || items == nil {
		return nil, "", false, fmt.Errorf("Slack collection lacks %s array", key)
	}
	var meta struct {
		Cursor string `json:"next_cursor"`
	}
	_ = json.Unmarshal(envelope["response_metadata"], &meta)
	var more bool
	_ = json.Unmarshal(envelope["has_more"], &more)
	if meta.Cursor != "" {
		more = true
	}
	next := meta.Cursor
	if more && next == "" {
		if len(items) == 0 {
			return nil, "", true, fmt.Errorf("Slack has_more without cursor or timestamp progress")
		}
		if path != "/conversations.history" && path != "/conversations.replies" {
			return nil, "", true, fmt.Errorf("Slack has_more without next_cursor")
		}
		var last map[string]any
		_ = json.Unmarshal(items[len(items)-1], &last)
		ts, _ := last["ts"].(string)
		if !store.ValidSlackTS(ts) {
			return nil, "", true, fmt.Errorf("Slack pagination lacks exact boundary timestamp")
		}
		if path == "/conversations.replies" {
			return items, "", true, nil
		}
		next = "time:" + ts
	}
	return items, next, more, nil
}

func wireParams(path string, p map[string]string, cursor string) map[string]string {
	out := Clone(p)
	delete(out, "cursor")
	delete(out, "_archive_run")
	switch {
	case strings.HasPrefix(cursor, "time:"):
		key := "latest"
		if path == "/conversations.replies" {
			key = "oldest"
		}
		out[key] = strings.TrimPrefix(cursor, "time:")
		out["inclusive"] = "false"
	case strings.HasPrefix(cursor, "page:"):
		out["page"] = strings.TrimPrefix(cursor, "page:")
	case cursor != "":
		out["cursor"] = cursor
	}
	return out
}

// Fetch persists each page before returning its continuation. maxPages=0 is
// unlimited. Resumable sync may reuse a committed prefix; ad-hoc continuation
// never turns a suffix-only fetch into a complete scan.
func (r *Reader) Fetch(ctx context.Context, path string, params map[string]string, all, resume bool, maxPages int) (Result, error) {
	p := Clone(params)
	if err := validate(path, p); err != nil {
		return Result{}, err
	}
	scope := observationScope(r.Identity, path, p)
	if resume {
		scope = ArchiveScope(r.Identity, path, p)
	}
	cursor := p["cursor"]
	if strings.HasPrefix(cursor, "local:") {
		return Result{}, fmt.Errorf("local continuation cannot be used upstream")
	}
	completePrefix := cursor == "" || resume
	out := Result{Items: make([]json.RawMessage, 0), Source: "live", WorkspaceID: r.Identity.WorkspaceID}
	visited := map[string]bool{}
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if visited[cursor] {
			return out, fmt.Errorf("Slack repeated a pagination cursor")
		}
		visited[cursor] = true
		raw, err := r.Client.GetNoCache(ctx, path, wireParams(path, p, cursor))
		if err != nil {
			return out, err
		}
		items, next, more, err := Decode(path, raw)
		if err != nil {
			return out, err
		}
		if next != "" && next == cursor && more {
			return out, fmt.Errorf("Slack pagination made no progress")
		}
		scan := store.SlackScan{Cursor: next, Complete: !more && completePrefix}
		var metadata struct {
			Limited bool `json:"is_limited"`
		}
		_ = json.Unmarshal(raw, &metadata)
		if metadata.Limited {
			scan.Complete = false
			scan.Reason = "retention_limited"
		}
		if more && next == "" {
			scan.Complete = false
			scan.Reason = "cursorless_reply_pagination_unverified"
		}
		if !completePrefix {
			scan.Reason = "continuation_without_verified_prefix"
		}
		if path == "/search.messages" {
			scan.Complete = false
			scan.Reason = "search_is_supplemental"
		}
		if path == "/conversations.history" || path == "/conversations.replies" || path == "/search.messages" {
			if err = r.Store.CommitSlackPage(ctx, r.Identity, p["channel"], path, scope, items, scan); err != nil {
				return out, err
			}
		} else {
			rt := "conversations"
			if path == "/users.list" {
				rt = "users"
			}
			if err = r.Store.CommitSlackDirectoryPage(ctx, r.Identity, rt, scope, items, scan); err != nil {
				return out, err
			}
		}
		out.Items = append(out.Items, items...)
		out.NextCursor = next
		out.HasMore = more
		out.Coverage = r.Store.SlackCoverage(scope)
		if !more || next == "" || !all || (maxPages > 0 && page+1 >= maxPages) {
			break
		}
		cursor = next
	}
	return out, nil
}

func Read(ctx context.Context, c *client.Client, dbPath, path string, params map[string]string, source string, all bool) (Result, error) {
	if source == "local" {
		if path == "/search.messages" {
			return Result{}, fmt.Errorf("messages uses live Slack search; use search --data-source local with explicit local filters")
		}
		if _, err := os.Stat(dbPath); err != nil {
			return Result{}, fmt.Errorf("local mirror unavailable; run sync --resources conversations,messages: %w", err)
		}
		s, err := store.OpenReadOnlyContext(ctx, dbPath)
		if err != nil {
			return Result{}, err
		}
		defer s.Close()
		return Local(ctx, s, path, params, all)
	}
	r, err := Open(ctx, c, dbPath)
	if err != nil {
		return Result{}, err
	}
	defer r.Store.Close()
	return r.Fetch(ctx, path, params, all, false, 0)
}

type localCursor struct{ Scope, TS, Channel, ID string }

func itemKey(raw json.RawMessage) localCursor {
	var item map[string]any
	_ = json.Unmarshal(raw, &item)
	key := localCursor{}
	key.TS, _ = item["ts"].(string)
	key.Channel, _ = item["channel"].(string)
	key.ID, _ = item["id"].(string)
	return key
}
func scopeHash(id store.SlackIdentity, path string, p map[string]string) string {
	sum := sha256.Sum256([]byte(Scope(id, path, p)))
	return hex.EncodeToString(sum[:])
}
func nextLocal(scope string, raw json.RawMessage) string {
	key := itemKey(raw)
	key.Scope = scope
	b, _ := json.Marshal(key)
	return "local:v1:" + base64.RawURLEncoding.EncodeToString(b)
}
