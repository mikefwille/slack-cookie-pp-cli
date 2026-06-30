package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestExtractResourceID_MessagesKeyOnTS(t *testing.T) {
	if got := ExtractResourceID("conversations-conversations-history", map[string]any{"ts": "1700000000.000100", "text": "hi"}); got != "1700000000.000100" {
		t.Errorf("history message id = %q, want ts value", got)
	}
	if got := ExtractResourceID("conversations-conversations-replies", map[string]any{"ts": "1700000000.000200"}); got != "1700000000.000200" {
		t.Errorf("replies message id = %q, want ts value", got)
	}
	// Conversations still resolve via the generic id fallback (unchanged).
	if got := ExtractResourceID("conversations", map[string]any{"id": "C123", "name": "general"}); got != "C123" {
		t.Errorf("conversation id = %q, want C123", got)
	}
}

func TestUpsertBatch_StoresMessageRows(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	items := []json.RawMessage{
		json.RawMessage(`{"ts":"1700000000.000100","text":"first"}`),
		json.RawMessage(`{"ts":"1700000000.000200","text":"second"}`),
	}
	stored, extractFailures, err := s.UpsertBatch("conversations-conversations-history", items)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 2 || extractFailures != 0 {
		t.Fatalf("stored=%d extractFailures=%d, want 2/0 (empty-store bug regression)", stored, extractFailures)
	}

	// Re-sync upserts the same rows keyed on ts: no duplicates.
	if _, _, err := s.UpsertBatch("conversations-conversations-history", items); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count("conversations-conversations-history"); n != 2 {
		t.Fatalf("count=%d, want 2 (re-sync should upsert, not duplicate)", n)
	}
}

func TestUpsertBatch_ConversationsListStoresPerChannel(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// The synced unit is the inner channel object, not the {ok,channels,...} wrapper.
	items := []json.RawMessage{
		json.RawMessage(`{"id":"C1","name":"general"}`),
		json.RawMessage(`{"id":"C2","name":"random"}`),
	}
	stored, fails, err := s.UpsertBatch("conversations", items)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 2 || fails != 0 {
		t.Fatalf("stored=%d fails=%d, want 2/0 (one row per channel)", stored, fails)
	}
}
