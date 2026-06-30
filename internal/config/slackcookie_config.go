// Hand-authored extension (survives regen): composed cookie auth + multi-account.
//
// Slack's web session auth is two credentials: the xoxc- token (sent as
// Authorization: Bearer, which the server accepts) and the d cookie (sent as
// Cookie: d=xoxd-...). Workspaces are separate logins, so each "account" is a
// named {workspace host, xoxc, d cookie} set. The generated client already
// sends every entry in cfg.Headers and uses cfg.BaseURL as the host, so wiring
// composed auth is purely a matter of populating those fields per active
// account. ApplyAccount is called from rootFlags.newClient after config.Load.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"slack-cookie-pp-cli/internal/cliutil"
)

// Account is one Slack workspace login.
type Account struct {
	Workspace string `toml:"workspace"`
	Xoxc      string `toml:"xoxc"`
	DCookie   string `toml:"d_cookie"`
	DSCookie  string `toml:"d_s_cookie"`
}

// accountsFile is the multi-account view of config.toml. The generated Config
// struct does not carry these tables, so we re-read the file for them.
type accountsFile struct {
	DefaultAccount string             `toml:"default_account"`
	Accounts       map[string]Account `toml:"accounts"`
}

// LoadAccountsFile parses the [accounts.*] tables and default_account from the
// resolved config path. Best-effort: a missing file or absent tables yields an
// empty result, not an error.
func LoadAccountsFile(path string) accountsFile {
	var af accountsFile
	if path == "" {
		return af
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &af)
	}
	return af
}

// AccountNames returns the configured account names.
func (af accountsFile) AccountNames() []string {
	names := make([]string, 0, len(af.Accounts))
	for n := range af.Accounts {
		names = append(names, n)
	}
	return names
}

// ResolveAccountName picks the active account: explicit flag, then SLACK_ACCOUNT
// env, then default_account, then "default".
func ResolveAccountName(explicit, defaultAccount string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	if v := strings.TrimSpace(os.Getenv("SLACK_ACCOUNT")); v != "" {
		return v
	}
	if strings.TrimSpace(defaultAccount) != "" {
		return strings.TrimSpace(defaultAccount)
	}
	return "default"
}

const accountsFileName = "accounts.toml"

// AccountsFilePath returns the dedicated multi-account store path: a private
// 0600 file in the data dir, separate from config.toml (which config.save()
// rewrites with only base_url+headers, silently wiping any [accounts.*]
// tables) and from the generated single-account credentials.toml. It holds
// default_account plus the [accounts.*] tables, each carrying that account's
// session secrets (xoxc token + d/d-s cookies).
func AccountsFilePath() (string, error) {
	dir, err := cliutil.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, accountsFileName), nil
}

func writeAccountsFile(path string, af accountsFile) error {
	data, err := toml.Marshal(af)
	if err != nil {
		return fmt.Errorf("marshaling accounts: %w", err)
	}
	return cliutil.AtomicWritePrivateFile(path, data, 0o600, 0o700)
}

// resolveAccountStore loads the multi-account store. The dedicated accounts
// file is canonical; a legacy [accounts.*] block in config.toml is read only
// as a fallback so a hand-written config still resolves.
func resolveAccountStore(configPath string) accountsFile {
	if path, err := AccountsFilePath(); err == nil {
		if af := LoadAccountsFile(path); len(af.Accounts) > 0 || strings.TrimSpace(af.DefaultAccount) != "" {
			return af
		}
	}
	return LoadAccountsFile(configPath)
}

// AccountList returns the configured accounts and the default account name,
// reading the dedicated accounts file first and falling back to a legacy
// [accounts.*] block in the given config file.
func AccountList(configPath string) (map[string]Account, string) {
	af := resolveAccountStore(configPath)
	return af.Accounts, af.DefaultAccount
}

// SaveAccount persists one account to the dedicated accounts file, creating
// the file if needed and preserving every existing account (round-trip, no
// clobber). The first account saved becomes the default; makeDefault forces
// it. Secrets are written 0600 and never touch config.toml.
func SaveAccount(name string, acct Account, makeDefault bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("account name is required")
	}
	path, err := AccountsFilePath()
	if err != nil {
		return err
	}
	af := LoadAccountsFile(path)
	if af.Accounts == nil {
		af.Accounts = map[string]Account{}
	}
	af.Accounts[name] = acct
	if makeDefault || strings.TrimSpace(af.DefaultAccount) == "" {
		af.DefaultAccount = name
	}
	return writeAccountsFile(path, af)
}

// DeleteAccount removes an account's stored secrets from the accounts file.
// It reports whether the account existed. When the deleted account was the
// default, the lexicographically-first remaining account is promoted (so the
// choice is deterministic) or the default is cleared if none remain.
func DeleteAccount(name string) (bool, error) {
	name = strings.TrimSpace(name)
	path, err := AccountsFilePath()
	if err != nil {
		return false, err
	}
	af := LoadAccountsFile(path)
	if _, ok := af.Accounts[name]; !ok {
		return false, nil
	}
	delete(af.Accounts, name)
	if af.DefaultAccount == name {
		af.DefaultAccount = ""
		names := af.AccountNames()
		sort.Strings(names)
		if len(names) > 0 {
			af.DefaultAccount = names[0]
		}
	}
	return true, writeAccountsFile(path, af)
}

// ApplyAccount resolves the active account from the dedicated accounts file
// (or a legacy [accounts.*] block in config.toml) and from flat env vars, then
// populates cfg so the generated client sends the right session: xoxc via
// Authorization: Bearer, d (+optional d-s) via the Cookie header, against the
// account's workspace host. Flat env vars win over the stored account. Returns
// the resolved account name.
//
// cfg here is the transient config built in newClient(); it is never saved, so
// injecting the Cookie header and BaseURL does not persist secrets to disk.
func ApplyAccount(cfg *Config, explicitAccount string) string {
	af := resolveAccountStore(cfg.Path)
	name := ResolveAccountName(explicitAccount, af.DefaultAccount)

	if acct, ok := af.Accounts[name]; ok {
		applyAccount(cfg, acct)
	}

	// Flat env overrides for single-account use, testing, and CI.
	if v := strings.TrimSpace(os.Getenv("SLACK_WORKSPACE")); v != "" {
		setWorkspaceHost(cfg, v)
	}
	if v := strings.TrimSpace(os.Getenv("SLACK_XOXC_TOKEN")); v != "" {
		cfg.SlackXoxcToken = v
	}
	dCookie := os.Getenv("SLACK_D_COOKIE")
	dsCookie := os.Getenv("SLACK_D_S_COOKIE")
	if strings.TrimSpace(dCookie) != "" || strings.TrimSpace(dsCookie) != "" {
		setCookieHeader(cfg, dCookie, dsCookie)
	}
	return name
}

func applyAccount(cfg *Config, a Account) {
	if a.Workspace != "" {
		setWorkspaceHost(cfg, a.Workspace)
	}
	if a.Xoxc != "" {
		cfg.SlackXoxcToken = a.Xoxc
	}
	if a.DCookie != "" || a.DSCookie != "" {
		setCookieHeader(cfg, a.DCookie, a.DSCookie)
	}
}

// setWorkspaceHost accepts "example", "example.slack.com", or a full URL and sets
// cfg.BaseURL to the workspace-scoped Slack API host. A bare name or a full
// URL pointing at a real (non-loopback) host always resolves to https so the
// session cookie can never be transmitted in cleartext after a typo.
func setWorkspaceHost(cfg *Config, ws string) {
	ws = strings.TrimSpace(ws)
	if ws == "" {
		return
	}
	if strings.Contains(ws, "://") {
		cfg.BaseURL = UpgradeInsecureHost(strings.TrimRight(ws, "/"))
		return
	}
	host := strings.TrimSuffix(ws, "/")
	if !strings.Contains(host, ".") {
		host = host + ".slack.com"
	}
	cfg.BaseURL = "https://" + host + "/api"
}

// UpgradeInsecureHost rewrites an http:// URL to https:// for non-loopback
// hosts so a typo'd workspace can never send the d cookie over cleartext.
// Loopback hosts (localhost, 127.0.0.1, ::1) keep http:// so local test and
// mock servers (SLACK_COOKIE_BASE_URL) still work. Exported so the auth-login
// bootstrap path (cli.workspaceWebURL) hardens the same way the live request
// path (setWorkspaceHost) does.
func UpgradeInsecureHost(raw string) string {
	const httpPrefix = "http://"
	if !strings.HasPrefix(raw, httpPrefix) {
		return raw
	}
	rest := raw[len(httpPrefix):]
	hostPort := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostPort = rest[:i]
	}
	host := hostPort
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		host = hostPort[:i]
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return raw
	}
	return "https://" + rest
}

// setCookieHeader sets the Cookie header from the d (and optional d-s) cookie,
// using the shared cliutil.CookieHeader builder.
func setCookieHeader(cfg *Config, d, ds string) {
	header := cliutil.CookieHeader(d, ds)
	if header == "" {
		return
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	cfg.Headers["Cookie"] = header
}
