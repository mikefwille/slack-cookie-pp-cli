package cli

import (
	"bytes"
	"strings"
	"testing"

	"slack-cookie-pp-cli/internal/config"
)

// runCLI drives the full command tree (including the novel commands wired by
// addSlackcookieCommands) and returns stdout, stderr, and the resulting error.
func runCLI(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := RootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// isolateAuthHome routes kind dirs at a temp dir and clears flat session env.
func isolateAuthHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	for _, k := range []string{
		"SLACK_COOKIE_DATA_DIR", "SLACK_COOKIE_CONFIG_DIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME",
		"SLACK_WORKSPACE", "SLACK_XOXC_TOKEN", "SLACK_D_COOKIE", "SLACK_D_S_COOKIE", "SLACK_ACCOUNT",
		"SLACK_COOKIE_CONFIG",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_COOKIE_HOME", tmp)
}

func TestSetTokenViaStdinNoWarning(t *testing.T) {
	isolateAuthHome(t)
	out, errb, err := runCLI(t, "xoxc=xoxc-secretAAAA\nd=xoxd-secretBBBB\n",
		"auth", "set-token", "--account", "work", "--workspace", "example")
	if err != nil {
		t.Fatalf("set-token: %v (stderr=%s)", err, errb)
	}
	if strings.Contains(errb, "warning") {
		t.Errorf("stdin path should not warn, got stderr: %s", errb)
	}
	if strings.Contains(out, "secretAAAA") || strings.Contains(out, "secretBBBB") {
		t.Errorf("raw secret leaked to output: %s", out)
	}
	accounts, def := config.AccountList("")
	if accounts["work"].Xoxc != "xoxc-secretAAAA" || def != "work" {
		t.Fatalf("account not saved correctly: %v default=%q", accounts, def)
	}
}

func TestSetTokenViaFlagWarns(t *testing.T) {
	isolateAuthHome(t)
	_, errb, err := runCLI(t, "",
		"auth", "set-token", "--account", "work", "--workspace", "example",
		"--xoxc", "xoxc-flagtoken", "--cookie", "xoxd-flagcookie")
	if err != nil {
		t.Fatalf("set-token: %v", err)
	}
	if !strings.Contains(errb, "warning") {
		t.Errorf("flag path should warn about argv leak, stderr: %q", errb)
	}
	if accounts, _ := config.AccountList(""); accounts["work"].Xoxc != "xoxc-flagtoken" {
		t.Errorf("flag-supplied account not saved: %v", accounts)
	}
}

func TestSetTokenMissingWorkspaceIsUsageError(t *testing.T) {
	isolateAuthHome(t)
	_, _, err := runCLI(t, "xoxc=x\nd=y\n", "auth", "set-token", "--account", "work")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("missing --workspace should exit 2, got %d (err=%v)", got, err)
	}
}

func TestSetTokenBareInvocationPrintsHelp(t *testing.T) {
	isolateAuthHome(t)
	out, _, err := runCLI(t, "", "auth", "set-token")
	if err != nil {
		t.Fatalf("bare set-token should exit 0, got %v", err)
	}
	if !strings.Contains(out, "Usage") && !strings.Contains(out, "set-token") {
		t.Errorf("bare set-token should print help, got: %s", out)
	}
}

func TestAuthListMasksSecrets(t *testing.T) {
	isolateAuthHome(t)
	if err := config.SaveAccount("work", config.Account{Workspace: "example", Xoxc: "xoxc-rawtokenZZZZ", DCookie: "xoxd-rawcookieYYYY"}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, _, err := runCLI(t, "", "auth", "list", "--json")
	if err != nil {
		t.Fatalf("auth list: %v", err)
	}
	if strings.Contains(out, "rawtokenZZZZ") || strings.Contains(out, "rawcookieYYYY") {
		t.Errorf("raw secret present in masked list output: %s", out)
	}
	if !strings.Contains(out, "****ZZZZ") {
		t.Errorf("expected masked tail in output: %s", out)
	}
}

func TestLoginBareInvocationPrintsHelp(t *testing.T) {
	isolateAuthHome(t)
	out, _, err := runCLI(t, "", "auth", "login")
	if err != nil {
		t.Fatalf("bare login should exit 0, got %v", err)
	}
	if !strings.Contains(out, "login") {
		t.Errorf("bare login should print help, got: %s", out)
	}
}

func TestLoginMissingCookieIsUsageError(t *testing.T) {
	isolateAuthHome(t)
	// --account + --workspace present but no cookie source: usage error (exit 2),
	// not a network attempt.
	_, _, err := runCLI(t, "", "auth", "login", "--account", "work", "--workspace", "example")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("login without a cookie should exit 2, got %d (err=%v)", got, err)
	}
}

func TestLogoutRemovesAccount(t *testing.T) {
	isolateAuthHome(t)
	_ = config.SaveAccount("work", config.Account{Workspace: "example", Xoxc: "x", DCookie: "d"}, true)
	_ = config.SaveAccount("personal", config.Account{Workspace: "home", Xoxc: "x", DCookie: "d"}, false)

	_, _, err := runCLI(t, "", "auth", "logout", "--account", "personal")
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	accounts, _ := config.AccountList("")
	if _, ok := accounts["personal"]; ok {
		t.Error("personal account should be removed")
	}
	if _, ok := accounts["work"]; !ok {
		t.Error("work account should remain")
	}
}
