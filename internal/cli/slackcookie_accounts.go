// Hand-authored extension (survives regen): multi-account enumeration and
// per-account data-file resolution shared by the auth, doctor, and offline
// commands.

package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"slack-cookie-pp-cli/internal/cliutil"
	"slack-cookie-pp-cli/internal/config"
)

// listAccounts returns the configured account names (sorted) and the default
// account name for the given config path. Reads the dedicated accounts file,
// falling back to a legacy [accounts.*] block in config.toml.
func listAccounts(configPath string) (names []string, defaultAccount string) {
	accounts, def := config.AccountList(configPath)
	names = make([]string, 0, len(accounts))
	for n := range accounts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, def
}

// accountsFor returns the full account map plus the default name, for commands
// that need each account's stored fields (e.g. auth list, per-account fan-out).
func accountsFor(configPath string) (map[string]config.Account, string) {
	return config.AccountList(configPath)
}

// resolveAccountSet returns the accounts a command should operate on. With
// allAccounts set it returns every configured account name; otherwise it
// returns the single resolved active account (explicit flag, $SLACK_ACCOUNT,
// default_account, then "default"). The returned slice is never empty so
// callers always have at least one account to act on.
func resolveAccountSet(configPath, explicitAccount string, allAccounts bool) []string {
	names, def := listAccounts(configPath)
	if allAccounts && len(names) > 0 {
		return names
	}
	active := config.ResolveAccountName(explicitAccount, def)
	return []string{active}
}

// accountDBPathResolver is the seam the generated defaultDBPath consults to
// route reads/writes to the active account's per-account SQLite file. Set once
// by addSlackcookieCommands (closing over rootFlags); nil leaves the generated
// single-DB behavior untouched. Mirrors the resolveTenantID var pattern.
var accountDBPathResolver func(defaultName string) string

// resolveActiveAccountName returns the active account name using the same
// precedence as the live request path: --account > $SLACK_ACCOUNT >
// default_account > "default".
func resolveActiveAccountName(flags *rootFlags) string {
	_, def := config.AccountList(flags.configPath)
	return config.ResolveAccountName(flags.account, def)
}

// sanitizeAccountFilename maps an account name to a safe single path segment:
// only [A-Za-z0-9_-] survive, everything else (including '.', '/', '\\') maps
// to '_', so no traversal or separator can escape the data dir.
func sanitizeAccountFilename(account string) string {
	account = strings.TrimSpace(account)
	var b strings.Builder
	for _, r := range account {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// accountDBPath resolves <data-dir>/<account>.db for a named account. The
// implicit single-account names ("" and "default") resolve to the canonical
// data.db so existing single-account mirrors keep working. A data-dir
// resolution failure returns "" so the caller falls back to defaultDBPath's
// own conservative path.
func accountDBPath(account string) string {
	safe := sanitizeAccountFilename(account)
	dir, err := cliutil.DataDir()
	if err != nil {
		return ""
	}
	if safe == "" || safe == "default" {
		return filepath.Join(dir, "data.db")
	}
	return filepath.Join(dir, safe+".db")
}

// collidingAccount returns the name of an existing account (other than
// candidate) whose sanitized DB filename matches candidate's, or "" if none.
// Two names that sanitize to the same file (e.g. "team.a" and "team_a") would
// silently share one mirror, merging separate-login workspaces and defeating
// the per-account isolation accountDBPath provides.
func collidingAccount(configPath, candidate string) string {
	want := sanitizeAccountFilename(candidate)
	if want == "" || want == "default" {
		return ""
	}
	names, _ := listAccounts(configPath)
	for _, n := range names {
		if n == candidate {
			continue
		}
		if sanitizeAccountFilename(n) == want {
			return n
		}
	}
	return ""
}

// installAccountDBResolver wires defaultDBPath to the active account's DB. The
// closure reads flags at call time (after flag parsing), so named accounts get
// <account>.db while the implicit default keeps the generated data.db path.
func installAccountDBResolver(flags *rootFlags) {
	accountDBPathResolver = func(defaultName string) string {
		name := resolveActiveAccountName(flags)
		if name == "" || name == "default" {
			return ""
		}
		return accountDBPath(name)
	}
}

// maskTail redacts all but the last 4 characters of a secret for safe display.
// An empty value renders as "(none)" so masked output never implies a secret
// is present when it is not.
func maskTail(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "(none)"
	}
	if len(secret) <= 4 {
		return "****"
	}
	return "****" + secret[len(secret)-4:]
}
