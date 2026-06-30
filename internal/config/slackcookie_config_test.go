package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points all kind dirs (config, data, …) at a temp dir and clears
// the flat session env vars so account-store tests never read the developer's
// real config or a leaked env credential.
func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("SLACK_COOKIE_HOME", tmp)
	for _, k := range []string{
		"SLACK_COOKIE_DATA_DIR", "SLACK_COOKIE_CONFIG_DIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME",
		"SLACK_WORKSPACE", "SLACK_XOXC_TOKEN", "SLACK_D_COOKIE", "SLACK_D_S_COOKIE", "SLACK_ACCOUNT",
		"SLACK_COOKIE_CONFIG", "SLACK_COOKIE_BASE_URL",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("SLACK_COOKIE_HOME", tmp)
	return tmp
}

func TestSaveAccountRoundTripNoClobber(t *testing.T) {
	isolateHome(t)

	if err := SaveAccount("personal", Account{Workspace: "home", Xoxc: "xoxc-p", DCookie: "xoxd-p"}, false); err != nil {
		t.Fatalf("SaveAccount personal: %v", err)
	}
	if err := SaveAccount("work", Account{Workspace: "example", Xoxc: "xoxc-w", DCookie: "xoxd-w"}, false); err != nil {
		t.Fatalf("SaveAccount work: %v", err)
	}

	accounts, def := AccountList("")
	if len(accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d (%v)", len(accounts), accounts)
	}
	if accounts["personal"].Xoxc != "xoxc-p" {
		t.Errorf("personal account clobbered: %+v", accounts["personal"])
	}
	if accounts["work"].Workspace != "example" {
		t.Errorf("work account wrong: %+v", accounts["work"])
	}
	if def != "personal" {
		t.Errorf("first saved account should be default, got %q", def)
	}
}

func TestSaveAccountDefaultRules(t *testing.T) {
	isolateHome(t)

	_ = SaveAccount("a", Account{Workspace: "wa", Xoxc: "x", DCookie: "d"}, false)
	if _, def := AccountList(""); def != "a" {
		t.Fatalf("first account should be default, got %q", def)
	}
	// A later account must not steal the default.
	_ = SaveAccount("b", Account{Workspace: "wb", Xoxc: "x", DCookie: "d"}, false)
	if _, def := AccountList(""); def != "a" {
		t.Fatalf("default should remain a, got %q", def)
	}
	// makeDefault forces it.
	_ = SaveAccount("b", Account{Workspace: "wb", Xoxc: "x", DCookie: "d"}, true)
	if _, def := AccountList(""); def != "b" {
		t.Fatalf("makeDefault should switch default to b, got %q", def)
	}
}

func TestDeleteAccountPromotesDefault(t *testing.T) {
	isolateHome(t)
	_ = SaveAccount("a", Account{Workspace: "wa", Xoxc: "x", DCookie: "d"}, true)
	_ = SaveAccount("b", Account{Workspace: "wb", Xoxc: "x", DCookie: "d"}, false)

	ok, err := DeleteAccount("a")
	if err != nil || !ok {
		t.Fatalf("DeleteAccount(a) = %v, %v", ok, err)
	}
	accounts, def := AccountList("")
	if _, present := accounts["a"]; present {
		t.Error("account a should be gone")
	}
	if def != "b" {
		t.Errorf("default should promote to b, got %q", def)
	}
	// Deleting a missing account reports false, no error.
	if ok, err := DeleteAccount("missing"); ok || err != nil {
		t.Errorf("DeleteAccount(missing) = %v, %v", ok, err)
	}
}

// TestConfigSaveDoesNotWipeAccounts guards the silent-wipe bug: config.save()
// serializes only base_url+headers into config.toml, so account secrets stored
// in the dedicated accounts file must survive a config write.
func TestConfigSaveDoesNotWipeAccounts(t *testing.T) {
	tmp := isolateHome(t)
	_ = SaveAccount("work", Account{Workspace: "example", Xoxc: "xoxc-w", DCookie: "xoxd-w"}, true)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.BaseURL = "https://example.slack.com/api"
	if err := cfg.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	accounts, def := AccountList("")
	if _, ok := accounts["work"]; !ok || def != "work" {
		t.Fatalf("config save wiped accounts: %v default=%q", accounts, def)
	}
	// And the secrets must not have leaked into config.toml.
	data, _ := os.ReadFile(filepath.Join(tmp, "config", "config.toml"))
	if strings.Contains(string(data), "xoxc-w") || strings.Contains(string(data), "xoxd-w") {
		t.Errorf("secrets leaked into config.toml: %s", data)
	}
}

func TestResolveAccountName(t *testing.T) {
	cases := []struct {
		name       string
		explicit   string
		envAccount string
		def        string
		want       string
	}{
		{"explicit wins", "flagacct", "envacct", "defacct", "flagacct"},
		{"env over default", "", "envacct", "defacct", "envacct"},
		{"default over fallback", "", "", "defacct", "defacct"},
		{"fallback default", "", "", "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SLACK_ACCOUNT", tc.envAccount)
			if got := ResolveAccountName(tc.explicit, tc.def); got != tc.want {
				t.Errorf("ResolveAccountName(%q,%q) env=%q = %q, want %q", tc.explicit, tc.def, tc.envAccount, got, tc.want)
			}
		})
	}
}

func TestSetWorkspaceHostHardensTransport(t *testing.T) {
	cases := []struct{ in, want string }{
		{"example", "https://example.slack.com/api"},
		{"example.slack.com", "https://example.slack.com/api"},
		{"https://example.slack.com/api", "https://example.slack.com/api"},
		{"http://evil.example.com/api", "https://evil.example.com/api"}, // cleartext upgraded
		{"http://127.0.0.1:8080/api", "http://127.0.0.1:8080/api"},      // loopback preserved
		{"http://localhost:9090", "http://localhost:9090"},              // loopback preserved
	}
	for _, tc := range cases {
		cfg := &Config{}
		setWorkspaceHost(cfg, tc.in)
		if cfg.BaseURL != tc.want {
			t.Errorf("setWorkspaceHost(%q) = %q, want %q", tc.in, cfg.BaseURL, tc.want)
		}
	}
}

func TestApplyAccountFromStore(t *testing.T) {
	isolateHome(t)
	_ = SaveAccount("work", Account{Workspace: "example", Xoxc: "xoxc-work", DCookie: "xoxd-work", DSCookie: "xoxds-work"}, true)

	cfg := &Config{BaseURL: "https://slack.com/api"}
	name := ApplyAccount(cfg, "work")
	if name != "work" {
		t.Fatalf("resolved account = %q, want work", name)
	}
	if cfg.SlackXoxcToken != "xoxc-work" {
		t.Errorf("xoxc not applied: %q", cfg.SlackXoxcToken)
	}
	if cfg.BaseURL != "https://example.slack.com/api" {
		t.Errorf("base URL not applied: %q", cfg.BaseURL)
	}
	cookie := cfg.Headers["Cookie"]
	if !strings.Contains(cookie, "d=xoxd-work") || !strings.Contains(cookie, "d-s=xoxds-work") {
		t.Errorf("cookie header wrong: %q", cookie)
	}
}
