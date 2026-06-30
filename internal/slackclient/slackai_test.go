package slackclient

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fakeDoer is a scripted transport: fixed Post responses by path and a
// sequence of conversations.replies snapshots returned in order.
type fakeDoer struct {
	posts      map[string]json.RawMessage
	postErr    map[string]error
	repliesSeq []json.RawMessage
	replyIdx   int
	postCalls  []string
}

func (f *fakeDoer) Post(_ context.Context, path string, _ any) (json.RawMessage, int, error) {
	f.postCalls = append(f.postCalls, path)
	if err := f.postErr[path]; err != nil {
		return nil, 0, err
	}
	if r, ok := f.posts[path]; ok {
		return r, 200, nil
	}
	return json.RawMessage(`{"ok":false,"error":"not_scripted"}`), 200, nil
}

func (f *fakeDoer) GetNoCache(_ context.Context, path string, _ map[string]string) (json.RawMessage, error) {
	if path == "/conversations.replies" && len(f.repliesSeq) > 0 {
		idx := f.replyIdx
		if idx >= len(f.repliesSeq) {
			idx = len(f.repliesSeq) - 1
		}
		f.replyIdx++
		return f.repliesSeq[idx], nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func askFixture() *fakeDoer {
	return &fakeDoer{
		posts: map[string]json.RawMessage{
			"/conversations.open": json.RawMessage(`{"ok":true,"channel":{"id":"D123"}}`),
			"/chat.postMessage":   json.RawMessage(`{"ok":true,"ts":"111.222"}`),
		},
	}
}

const (
	replyParentOnly = `{"ok":true,"messages":[{"ts":"111.222","text":"my question"}]}`
	replyWithAnswer = `{"ok":true,"messages":[{"ts":"111.222","text":"my question"},{"ts":"111.333","text":"the answer"}]}`
)

func TestAskSettlesWhenReplyStable(t *testing.T) {
	d := askFixture()
	d.repliesSeq = []json.RawMessage{
		json.RawMessage(replyParentOnly),
		json.RawMessage(replyWithAnswer),
		json.RawMessage(replyWithAnswer), // unchanged across two polls => settled
	}
	res, err := New(d).Ask(context.Background(), "what did I miss?", AskOptions{
		PollInterval: time.Millisecond,
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if res.Answer != "the answer" {
		t.Errorf("answer = %q, want %q", res.Answer, "the answer")
	}
	if !res.Settled || res.TimedOut {
		t.Errorf("expected settled, got settled=%v timedOut=%v", res.Settled, res.TimedOut)
	}
	if res.Channel != "D123" || res.ThreadTS != "111.222" {
		t.Errorf("channel/ts wrong: %q %q", res.Channel, res.ThreadTS)
	}
}

func TestAskTimesOutWithoutHanging(t *testing.T) {
	d := askFixture()
	// The assistant never replies (only the parent message ever present).
	d.repliesSeq = []json.RawMessage{json.RawMessage(replyParentOnly)}
	res, err := New(d).Ask(context.Background(), "q", AskOptions{
		PollInterval: 5 * time.Millisecond,
		Timeout:      25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !res.TimedOut {
		t.Errorf("expected TimedOut, got %+v", res)
	}
	if res.Answer != "" {
		t.Errorf("expected empty answer on timeout, got %q", res.Answer)
	}
}

func TestAskFailsWhenPostRejected(t *testing.T) {
	d := askFixture()
	d.posts["/chat.postMessage"] = json.RawMessage(`{"ok":false,"error":"channel_not_found"}`)
	if _, err := New(d).Ask(context.Background(), "q", AskOptions{Timeout: time.Second}); err == nil {
		t.Fatal("expected error when chat.postMessage returns ok:false")
	}
}

func TestSearchInlineAvailableAndDegraded(t *testing.T) {
	// ok:true with an answer field.
	d := &fakeDoer{posts: map[string]json.RawMessage{
		"/search.inline": json.RawMessage(`{"ok":true,"answer":"42"}`),
	}}
	res, err := New(d).SearchInline(context.Background(), "meaning of life")
	if err != nil || !res.Available || res.Answer != "42" {
		t.Fatalf("available case: %+v err=%v", res, err)
	}

	// ok:false degrades to unavailable, no error.
	d2 := &fakeDoer{posts: map[string]json.RawMessage{
		"/search.inline": json.RawMessage(`{"ok":false,"error":"feature_not_enabled"}`),
	}}
	res2, err := New(d2).SearchInline(context.Background(), "x")
	if err != nil {
		t.Fatalf("degrade should not error: %v", err)
	}
	if res2.Available {
		t.Errorf("expected Available=false on ok:false")
	}
}

func TestSkillsAndDigestDegrade(t *testing.T) {
	d := &fakeDoer{posts: map[string]json.RawMessage{
		"/ai.alpha.slackbot.skills.listEnabled": json.RawMessage(`{"ok":true,"skills":["recap","search"]}`),
		"/ai.alpha.digest.listDigestChannelIds": json.RawMessage(`{"ok":false,"error":"not_allowed"}`),
	}}
	skills, _ := New(d).Skills(context.Background())
	if !skills.Available || len(skills.Skills) != 2 {
		t.Errorf("skills = %+v", skills)
	}
	digest, _ := New(d).DigestChannels(context.Background())
	if digest.Available {
		t.Errorf("digest should degrade: %+v", digest)
	}
}
