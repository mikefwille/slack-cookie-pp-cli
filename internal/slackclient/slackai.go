// Package slackclient isolates the Slack AI surface (the Slackbot assistant
// plus the undocumented native-AI endpoints) behind one client so a Slack-side
// change to any of them cannot break the unrelated generated commands.
//
// ai ask rides documented methods (chat.postMessage + conversations.replies)
// and is the robust headline path. search.inline, ai.alpha.digest.*, and
// ai.alpha.slackbot.skills.listEnabled are undocumented internals discovered by
// live sniff (method name only, n=1); each degrades cleanly to a clear
// "not available here" rather than erroring (see docs/discovery/slack-ai-sniff.md).
package slackclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SlackbotUserID is the well-known bot user id for Slackbot, present in every
// workspace. conversations.open against it returns the assistant IM channel.
const SlackbotUserID = "USLACKBOT"

// Doer is the subset of *client.Client the AI surface needs. Defining it as an
// interface keeps slackclient testable with a fake transport and avoids a hard
// dependency on the concrete client in unit tests. *client.Client satisfies it.
type Doer interface {
	Post(ctx context.Context, path string, body any) (json.RawMessage, int, error)
	GetNoCache(ctx context.Context, path string, params map[string]string) (json.RawMessage, error)
}

// AI wraps a transport with the Slack AI orchestration.
type AI struct {
	Doer Doer
}

// New returns an AI bound to the given transport.
func New(d Doer) *AI { return &AI{Doer: d} }

// okEnvelope is the common Slack response head. ok:false carries error in
// `error`; that is enough to decide success vs. graceful degrade without
// knowing the rest of an undocumented body shape.
type okEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func decodeOK(raw json.RawMessage) okEnvelope {
	var e okEnvelope
	_ = json.Unmarshal(raw, &e)
	return e
}

// AskOptions tunes the post-and-poll loop.
type AskOptions struct {
	PollInterval time.Duration
	Timeout      time.Duration
}

func (o AskOptions) withDefaults() AskOptions {
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	return o
}

// AskResult is the outcome of an ai ask exchange.
type AskResult struct {
	Question string            `json:"question"`
	Answer   string            `json:"answer"`
	Channel  string            `json:"channel"`
	ThreadTS string            `json:"thread_ts"`
	Settled  bool              `json:"settled"`
	TimedOut bool              `json:"timed_out"`
	Replies  []json.RawMessage `json:"replies,omitempty"`
}

// Ask posts a question into the Slackbot assistant IM and polls the thread
// until the assistant's reply stops changing or the timeout elapses. It uses
// only documented methods. A timeout returns the partial answer with TimedOut
// set rather than erroring, so the caller can still show progress.
func (a *AI) Ask(ctx context.Context, question string, opts AskOptions) (AskResult, error) {
	opts = opts.withDefaults()
	res := AskResult{Question: question}

	channel, err := a.openSlackbotIM(ctx)
	if err != nil {
		return res, err
	}
	res.Channel = channel

	postBody := map[string]any{"channel": channel, "text": question}
	raw, status, err := a.Doer.Post(ctx, "/chat.postMessage", postBody)
	if err != nil {
		return res, fmt.Errorf("posting question: %w", err)
	}
	if env := decodeOK(raw); !env.OK {
		return res, fmt.Errorf("chat.postMessage failed (HTTP %d): %s", status, slackErr(env))
	}
	var posted struct {
		TS string `json:"ts"`
	}
	_ = json.Unmarshal(raw, &posted)
	if posted.TS == "" {
		return res, fmt.Errorf("chat.postMessage returned no message ts")
	}
	res.ThreadTS = posted.TS

	deadline := time.Now().Add(opts.Timeout)
	prev := ""
	for {
		replies, err := a.fetchReplies(ctx, channel, posted.TS)
		if err == nil {
			filtered := filterReplies(replies, posted.TS)
			combined := combineReplyText(filtered)
			res.Replies = filtered
			res.Answer = combined
			if combined != "" {
				if combined == prev {
					// Settled: the same non-empty answer on two consecutive polls.
					res.Settled = true
					return res, nil
				}
				prev = combined
			}
		}
		if !time.Now().Before(deadline) {
			res.TimedOut = true
			return res, nil
		}
		if err := sleepCtx(ctx, opts.PollInterval); err != nil {
			return res, err
		}
	}
}

func (a *AI) openSlackbotIM(ctx context.Context) (string, error) {
	raw, status, err := a.Doer.Post(ctx, "/conversations.open", map[string]any{
		"users":     SlackbotUserID,
		"return_im": true,
	})
	if err != nil {
		return "", fmt.Errorf("opening Slackbot IM: %w", err)
	}
	if env := decodeOK(raw); !env.OK {
		return "", fmt.Errorf("conversations.open failed (HTTP %d): %s", status, slackErr(env))
	}
	var out struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Channel.ID == "" {
		return "", fmt.Errorf("conversations.open returned no channel id")
	}
	return out.Channel.ID, nil
}

func (a *AI) fetchReplies(ctx context.Context, channel, ts string) ([]json.RawMessage, error) {
	// GetNoCache: the 5-minute response cache would otherwise pin the poll on
	// the first (reply-less) snapshot and never observe the assistant answer.
	raw, err := a.Doer.GetNoCache(ctx, "/conversations.replies", map[string]string{
		"channel": channel,
		"ts":      ts,
	})
	if err != nil {
		return nil, err
	}
	if env := decodeOK(raw); !env.OK {
		return nil, fmt.Errorf("conversations.replies: %s", slackErr(env))
	}
	var out struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// filterReplies returns the thread messages that are not the user's own posted
// question (ts != postedTS) — i.e. the assistant's reply messages.
func filterReplies(messages []json.RawMessage, postedTS string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(messages))
	for _, m := range messages {
		var meta struct {
			TS string `json:"ts"`
		}
		_ = json.Unmarshal(m, &meta)
		if meta.TS != postedTS {
			out = append(out, m)
		}
	}
	return out
}

// combineReplyText joins the assistant reply texts from already-filtered
// messages (the caller filters once to avoid re-unmarshaling each poll).
func combineReplyText(filtered []json.RawMessage) string {
	var parts []string
	for _, m := range filtered {
		var meta struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(m, &meta)
		if t := strings.TrimSpace(meta.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// InlineResult is the (best-effort) parse of search.inline's AI answer card.
type InlineResult struct {
	Answer    string          `json:"answer"`
	Available bool            `json:"available"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// SearchInline calls the undocumented search.inline endpoint. A non-2xx or
// ok:false response degrades to Available=false with no error, so a Slack-side
// change cannot turn this into a hard failure. The answer text is extracted
// best-effort from common fields; Raw carries the full body for --json.
func (a *AI) SearchInline(ctx context.Context, query string) (InlineResult, error) {
	raw, _, err := a.Doer.Post(ctx, "/search.inline", map[string]any{"query": query})
	if err != nil {
		return InlineResult{Available: false}, nil
	}
	if env := decodeOK(raw); !env.OK {
		return InlineResult{Available: false, Raw: raw}, nil
	}
	return InlineResult{Available: true, Answer: extractAnswerText(raw), Raw: raw}, nil
}

// RecapResult carries the digest-channel listing (when the undocumented digest
// endpoint is available) plus whether it degraded.
type RecapResult struct {
	Available  bool            `json:"available"`
	ChannelIDs []string        `json:"channel_ids,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// DigestChannels calls ai.alpha.digest.listDigestChannelIds. Degrades to
// Available=false on any failure. The recap *content* endpoint shape is
// unknown (n=1 sniff), so the command layer pairs this with an ai ask
// fallback for the actual recap text.
func (a *AI) DigestChannels(ctx context.Context) (RecapResult, error) {
	raw, _, err := a.Doer.Post(ctx, "/ai.alpha.digest.listDigestChannelIds", map[string]any{})
	if err != nil {
		return RecapResult{Available: false}, nil
	}
	if env := decodeOK(raw); !env.OK {
		return RecapResult{Available: false, Raw: raw}, nil
	}
	return RecapResult{Available: true, ChannelIDs: extractChannelIDs(raw), Raw: raw}, nil
}

// SkillsResult lists the assistant's enabled skills (best-effort).
type SkillsResult struct {
	Available bool            `json:"available"`
	Skills    []string        `json:"skills,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Skills calls ai.alpha.slackbot.skills.listEnabled. Degrades on failure.
func (a *AI) Skills(ctx context.Context) (SkillsResult, error) {
	raw, _, err := a.Doer.Post(ctx, "/ai.alpha.slackbot.skills.listEnabled", map[string]any{})
	if err != nil {
		return SkillsResult{Available: false}, nil
	}
	if env := decodeOK(raw); !env.OK {
		return SkillsResult{Available: false, Raw: raw}, nil
	}
	return SkillsResult{Available: true, Skills: extractSkills(raw), Raw: raw}, nil
}

func slackErr(e okEnvelope) string {
	if strings.TrimSpace(e.Error) != "" {
		return e.Error
	}
	return "ok:false"
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// extractAnswerText pulls a human answer string out of an unknown-shape body by
// probing the field names most likely to hold the synthesized answer.
func extractAnswerText(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, key := range []string{"answer", "text", "summary", "content", "markdown"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	// Nested {module:{answer}} / {inline:{answer}} shapes.
	for _, key := range []string{"module", "inline", "ai_answer", "answer_block"} {
		if sub, ok := m[key].(map[string]any); ok {
			for _, k2 := range []string{"answer", "text", "summary", "content"} {
				if s, ok := sub[k2].(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
	}
	return ""
}

func extractChannelIDs(raw json.RawMessage) []string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for _, key := range []string{"channel_ids", "channels", "digest_channel_ids", "ids"} {
		if arr, ok := m[key].([]any); ok {
			return stringSlice(arr)
		}
	}
	return nil
}

func extractSkills(raw json.RawMessage) []string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for _, key := range []string{"skills", "enabled", "enabled_skills"} {
		if arr, ok := m[key].([]any); ok {
			out := make([]string, 0, len(arr))
			for _, v := range arr {
				switch t := v.(type) {
				case string:
					out = append(out, t)
				case map[string]any:
					for _, nk := range []string{"name", "title", "id", "skill"} {
						if s, ok := t[nk].(string); ok && s != "" {
							out = append(out, s)
							break
						}
					}
				}
			}
			return out
		}
	}
	return nil
}

func stringSlice(arr []any) []string {
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
