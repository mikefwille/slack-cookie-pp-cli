package cli

import (
	"strings"
	"testing"
)

func TestAIAskBareInvocationPrintsHelp(t *testing.T) {
	out, _, err := runCLI(t, "", "ai", "ask")
	if err != nil {
		t.Fatalf("bare ai ask should exit 0, got %v", err)
	}
	if !strings.Contains(out, "ask") {
		t.Errorf("bare ai ask should print help, got: %s", out)
	}
}

func TestAIAskDryRunPostsNothing(t *testing.T) {
	_, errb, err := runCLI(t, "", "ai", "ask", "--dry-run", "what did I miss")
	if err != nil {
		t.Fatalf("dry-run ai ask: %v", err)
	}
	if !strings.Contains(errb, "dry run") || !strings.Contains(errb, "what did I miss") {
		t.Errorf("dry-run should describe the would-be post, stderr: %s", errb)
	}
}

func TestAIAskVerifyShortCircuit(t *testing.T) {
	t.Setenv("PRINTING_PRESS_VERIFY", "1")
	t.Setenv("PRINTING_PRESS_VERIFY_LIVE_HTTP", "")
	out, _, err := runCLI(t, "", "ai", "ask", "--json", "hello")
	if err != nil {
		t.Fatalf("verify short-circuit should succeed, got %v", err)
	}
	if !strings.Contains(out, "verify_short_circuit") {
		t.Errorf("expected verify short-circuit envelope, got: %s", out)
	}
}

func TestAISearchBareInvocationPrintsHelp(t *testing.T) {
	out, _, err := runCLI(t, "", "ai", "search")
	if err != nil {
		t.Fatalf("bare ai search should exit 0, got %v", err)
	}
	if !strings.Contains(out, "search") {
		t.Errorf("bare ai search should print help, got: %s", out)
	}
}
