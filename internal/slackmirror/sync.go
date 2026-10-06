package slackmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"slack-cookie-pp-cli/internal/store"
)

type ArchiveOptions struct {
	Resources []string
	Full      bool
	Since     string
	Oldest    string
	MaxPages  int
	Params    map[string]map[string]string
}
type ArchiveReport struct {
	Coverage       store.SlackCoverage `json:"coverage"`
	WorkspaceID    string              `json:"workspace_id"`
	Observations   int                 `json:"observations"`
	WindowMessages int                 `json:"window_messages"`
	Oldest         string              `json:"oldest,omitempty"`
	Latest         string              `json:"latest"`
	Resumed        bool                `json:"resumed"`
	Conversations  int                 `json:"conversations"`
	Reasons        []string            `json:"reasons,omitempty"`
}
type archiveProgress struct{ Oldest, Latest string }

func Stamp(t time.Time) string { return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000) }

func (r *Reader) Partition(ctx context.Context, path string, params map[string]string, full bool, maxPages int) (Result, error) {
	p := Clone(params)
	if err := validate(path, p); err != nil {
		return Result{}, err
	}
	scope := ArchiveScope(r.Identity, path, p)
	scan, err := r.Store.SlackScan(scope)
	if err != nil {
		return Result{}, err
	}
	if full {
		scan = store.SlackScan{}
		if err = r.Store.SaveSlackScan(scope, scan); err != nil {
			return Result{}, err
		}
	}
	if scan.Complete {
		return Result{Items: []json.RawMessage{}, Coverage: r.Store.SlackCoverage(scope), Source: "live", WorkspaceID: r.Identity.WorkspaceID}, nil
	}
	p["cursor"] = scan.Cursor
	out, err := r.Fetch(ctx, path, p, true, true, maxPages)
	var api *APIError
	if errors.As(err, &api) && api.Code == "invalid_cursor" && scan.Cursor != "" {
		// Expired opaque cursors restart the same immutable range. Canonical keys
		// and revision ordering make every already committed page safe to replay.
		delete(p, "cursor")
		if resetErr := r.Store.SaveSlackScan(scope, store.SlackScan{Reason: "cursor_expired_restart", RetentionLimited: scan.RetentionLimited}); resetErr != nil {
			return out, resetErr
		}
		out, err = r.Fetch(ctx, path, p, true, true, maxPages)
	}
	if err != nil {
		current, _ := r.Store.SlackScan(scope)
		current.Complete = false
		current.Reason = "source_unavailable"
		if errors.As(err, &api) {
			current.Reason = api.Code
		}
		if saveErr := r.Store.SaveSlackScan(scope, current); saveErr != nil {
			return out, saveErr
		}
	} else if out.HasMore && maxPages > 0 && out.NextCursor != "" {
		current, _ := r.Store.SlackScan(scope)
		if current.Reason == "" {
			current.Reason = "page_limit_reached"
		}
		if err = r.Store.SaveSlackScan(scope, current); err != nil {
			return out, err
		}
		out.Coverage = r.Store.SlackCoverage(scope)
	}
	return out, err
}

func (r *Reader) Archive(ctx context.Context, opts ArchiveOptions) (ArchiveReport, error) {
	report := ArchiveReport{WorkspaceID: r.Identity.WorkspaceID, Coverage: store.SlackCoverage{State: "partial", Deletions: "unavailable"}}
	requested := map[string]bool{}
	for _, name := range opts.Resources {
		requested[name] = true
	}
	messages := requested["messages"]
	for name := range requested {
		if name != "messages" && name != "conversations" && name != "users" {
			return report, fmt.Errorf("sync resource %q requires an explicit channel/thread read; use conversations history/replies --all or sync --resources conversations,messages", name)
		}
	}
	paramsFor := func(name string, defaults map[string]string) map[string]string {
		p := Clone(defaults)
		for k, v := range opts.Params[name] {
			p[k] = v
		}
		return p
	}
	discovery := paramsFor("conversations", map[string]string{"types": "public_channel,private_channel,im,mpim", "exclude_archived": "false", "limit": "200"})
	progress := archiveProgress{Oldest: opts.Oldest, Latest: Stamp(time.Now())}
	controlParams := map[string]string{"since": opts.Since, "discovery": Scope(r.Identity, "/conversations.list", discovery)}
	serialized, _ := json.Marshal(opts.Params)
	controlParams["params"] = string(serialized)
	resourceSet, _ := json.Marshal(requested)
	controlParams["resources"] = string(resourceSet)
	control := Scope(r.Identity, "/archive-run", controlParams)
	state, err := r.Store.SlackScan(control)
	if err != nil {
		return report, err
	}
	newRun := opts.Full || state.Complete || state.Cursor == ""
	if !newRun {
		if err = json.Unmarshal([]byte(state.Cursor), &progress); err != nil {
			return report, fmt.Errorf("invalid archive checkpoint")
		}
	}
	if !store.ValidSlackTS(progress.Latest) || (progress.Oldest != "" && !store.ValidSlackTS(progress.Oldest)) {
		return report, fmt.Errorf("invalid archive range checkpoint")
	}
	report.Oldest, report.Latest, report.Resumed = progress.Oldest, progress.Latest, !newRun
	discovery["_archive_run"] = control + "|" + progress.Latest
	encoded, _ := json.Marshal(progress)
	if err = r.Store.SaveSlackScan(control, store.SlackScan{Cursor: string(encoded)}); err != nil {
		return report, err
	}
	complete := true
	record := func(label string, result Result, e error) {
		if e != nil || result.Coverage.State != "complete" {
			complete = false
			reason := label + ":incomplete"
			if e != nil {
				reason = label + ":source_unavailable"
			}
			report.Reasons = append(report.Reasons, reason)
		}
	}
	if requested["conversations"] || messages {
		result, e := r.Partition(ctx, "/conversations.list", discovery, newRun, opts.MaxPages)
		record("conversations", result, e)
		if e != nil {
			report.Coverage.Reasons = report.Reasons
			return report, e
		}
		// Discovery must finish before an archive can claim parent coverage. The
		// already stored prefix is useful, but does not define the entire universe.
		if result.HasMore {
			report.Coverage.Reasons = report.Reasons
			return report, nil
		}
	}
	if requested["users"] {
		users := paramsFor("users", map[string]string{"limit": "200"})
		users["_archive_run"] = control + "|" + progress.Latest
		result, e := r.Partition(ctx, "/users.list", users, newRun, opts.MaxPages)
		record("users", result, e)
		if e != nil {
			report.Coverage.Reasons = report.Reasons
			return report, e
		}
	}
	if messages {
		conversations, err := r.Store.SlackArchiveMembers(ctx, ArchiveScope(r.Identity, "/conversations.list", discovery))
		if err != nil {
			return report, err
		}
		for _, raw := range conversations {
			if err = ctx.Err(); err != nil {
				return report, err
			}
			var conversation map[string]any
			if err = json.Unmarshal(raw, &conversation); err != nil {
				return report, err
			}
			channel, _ := conversation["id"].(string)
			if channel == "" {
				return report, fmt.Errorf("conversation lacks identity")
			}
			// Honor the same discovery filter when cached metadata includes channels
			// from earlier, differently scoped discovery runs.
			if discovery["channel"] != "" && discovery["channel"] != channel {
				continue
			}
			if discovery["exclude_archived"] == "true" && conversation["is_archived"] == true {
				continue
			}
			typ := "public_channel"
			if conversation["is_private"] == true {
				typ = "private_channel"
			}
			if conversation["is_im"] == true {
				typ = "im"
			}
			if conversation["is_mpim"] == true {
				typ = "mpim"
			}
			if !containsType(discovery["types"], typ) {
				continue
			}
			report.Conversations++
			history := paramsFor("messages", map[string]string{"channel": channel, "limit": "100", "latest": progress.Latest})
			// --since narrows message selection, never retained-root discovery.
			delete(history, "oldest")
			history["channel"] = channel
			history["latest"] = progress.Latest
			result, e := r.Partition(ctx, "/conversations.history", history, false, opts.MaxPages)
			record(channel+":history", result, e)
			report.Observations += len(result.Items)
			if e != nil || result.HasMore {
				continue
			}
			roots, err := r.Store.QuerySlackMessages(ctx, store.SlackQuery{Channel: channel, Latest: progress.Latest, Endpoint: "/conversations.history"})
			if err != nil {
				return report, err
			}
			for _, raw := range roots {
				var root map[string]any
				_ = json.Unmarshal(raw, &root)
				ts, _ := root["ts"].(string)
				thread, _ := root["thread_ts"].(string)
				count, _ := root["reply_count"].(float64)
				if count <= 0 && thread != ts {
					continue
				}
				replies := map[string]string{"channel": channel, "ts": ts, "limit": "100", "latest": progress.Latest, "oldest": progress.Oldest, "inclusive": "false"}
				result, e := r.Partition(ctx, "/conversations.replies", replies, false, opts.MaxPages)
				record(channel+":thread:"+ts, result, e)
				report.Observations += len(result.Items)
			}
		}
		for _, raw := range conversations {
			var conversation map[string]any
			_ = json.Unmarshal(raw, &conversation)
			channel, _ := conversation["id"].(string)
			window, err := r.Store.QuerySlackMessages(ctx, store.SlackQuery{Channel: channel, Oldest: progress.Oldest, Latest: progress.Latest})
			if err != nil {
				return report, err
			}
			report.WindowMessages += len(window)
		}
		coverageScope := ArchiveScope(r.Identity, "/archive", map[string]string{"oldest": progress.Oldest, "latest": progress.Latest, "discovery": Scope(r.Identity, "/conversations.list", discovery)})
		reason := ""
		if !complete {
			reason = "archive_incomplete"
		}
		if err = r.Store.SaveSlackScan(coverageScope, store.SlackScan{Complete: complete, Reason: reason}); err != nil {
			return report, err
		}
		report.Coverage = r.Store.SlackCoverage(coverageScope)
		report.Coverage.Oldest = progress.Oldest
		report.Coverage.Latest = progress.Latest
	} else if complete {
		report.Coverage.State = "complete"
	}
	reason := ""
	if !complete {
		reason = "archive_incomplete"
	}
	if err = r.Store.SaveSlackScan(control, store.SlackScan{Cursor: string(encoded), Complete: complete, Reason: reason}); err != nil {
		return report, err
	}
	report.Coverage.Reasons = append(report.Coverage.Reasons, report.Reasons...)
	return report, nil
}

func containsType(types, typ string) bool {
	if types == "" {
		return true
	}
	for _, t := range strings.Split(types, ",") {
		if strings.TrimSpace(t) == typ {
			return true
		}
	}
	return false
}
