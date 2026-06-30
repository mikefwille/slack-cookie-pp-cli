package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountDBPathDistinctAndDefault(t *testing.T) {
	t.Setenv("SLACK_COOKIE_HOME", t.TempDir())

	work := accountDBPath("work")
	personal := accountDBPath("personal")
	if work == personal {
		t.Fatalf("per-account DBs should differ: %q == %q", work, personal)
	}
	if filepath.Base(work) != "work.db" || filepath.Base(personal) != "personal.db" {
		t.Errorf("unexpected names: %q %q", work, personal)
	}
	// Implicit single-account names map to the canonical data.db (back-compat).
	for _, name := range []string{"", "default"} {
		if got := filepath.Base(accountDBPath(name)); got != "data.db" {
			t.Errorf("accountDBPath(%q) base = %q, want data.db", name, got)
		}
	}
}

func TestSanitizeAccountFilename(t *testing.T) {
	cases := map[string]string{
		"work":             "work",
		"work-team_1":      "work-team_1",
		"../../etc/passwd": "______etc_passwd",
		"a/b\\c":           "a_b_c",
		"team.with.dots":   "team_with_dots",
		"  spaced  ":       "spaced",
	}
	for in, want := range cases {
		got := sanitizeAccountFilename(in)
		if got != want {
			t.Errorf("sanitizeAccountFilename(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, "/\\.") {
			t.Errorf("sanitized %q still contains a separator/dot: %q", in, got)
		}
	}
}

func TestDefaultDBPathRoutesThroughResolver(t *testing.T) {
	t.Setenv("SLACK_COOKIE_HOME", t.TempDir())
	defer func() { accountDBPathResolver = nil }()

	// Named active account → per-account DB.
	installAccountDBResolver(&rootFlags{account: "work"})
	if got := filepath.Base(defaultDBPath("slack-cookie-pp-cli")); got != "work.db" {
		t.Errorf("named account defaultDBPath base = %q, want work.db", got)
	}

	// Implicit default account → original data.db path.
	installAccountDBResolver(&rootFlags{account: ""})
	if got := filepath.Base(defaultDBPath("slack-cookie-pp-cli")); got != "data.db" {
		t.Errorf("default account defaultDBPath base = %q, want data.db", got)
	}
}
