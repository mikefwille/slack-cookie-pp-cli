// Hand-authored extension (survives regen): account-aware auth commands.
//
// These replace the generated single-credential auth set-token/status/logout
// with multi-account versions that persist each Slack workspace login to the
// dedicated accounts file (config.AccountsFilePath, 0600) instead of the
// single credentials.toml. Session secrets (the xoxc token and the d/d-s
// cookies) are read from stdin or env vars so they never land in argv (which
// leaks into the process table and shell history); a --xoxc/--cookie flag is a
// warned fallback only. Wiring lives in slackcookie_commands.go.

package cli

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"slack-cookie-pp-cli/internal/config"
	"slack-cookie-pp-cli/internal/slackclient"
)

// stdinIsReadable reports whether r holds piped/redirected data we can read
// without blocking. A real terminal (os.Stdin as a char device) returns false
// so an interactive invocation never hangs waiting on input; test buffers and
// pipes return true.
func stdinIsReadable(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// parseSecretBlob parses a key=value stdin blob (one per line) into a map.
// Recognized keys: xoxc, d, d-s (also d_s). Blank lines and lines beginning
// with '#' are ignored. Values keep their inner content verbatim after the
// first '='.
func parseSecretBlob(data string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "d_s" {
			key = "d-s"
		}
		out[key] = strings.TrimSpace(v)
	}
	return out
}

// readSecretBlob reads a key=value secret blob from stdin when it is piped or
// redirected (never blocks on an interactive terminal).
func readSecretBlob(cmd *cobra.Command) map[string]string {
	if !stdinIsReadable(cmd.InOrStdin()) {
		return map[string]string{}
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return map[string]string{}
	}
	return parseSecretBlob(string(data))
}

// resolveSecret applies the precedence: explicit flag value (warned), then the
// env var, then the parsed stdin blob. It reports through warned when a flag
// value was used so the caller can print a single "prefer stdin/env" warning.
func resolveSecret(flagVal, envName, blobKey string, blob map[string]string, warned *bool) string {
	if v := strings.TrimSpace(flagVal); v != "" {
		*warned = true
		return v
	}
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v
	}
	return strings.TrimSpace(blob[blobKey])
}

func newSlackcookieAuthSetTokenCmd(flags *rootFlags) *cobra.Command {
	var (
		account    string
		workspace  string
		xoxcFlag   string
		cookieFlag string
		dsFlag     string
		makeDef    bool
	)
	cmd := &cobra.Command{
		Use:   "set-token",
		Short: "Save a Slack account's session (xoxc token + d cookie) to the accounts file",
		Long: `Save a Slack workspace login (xoxc token + d cookie) as a named account.

Provide secrets via stdin or env vars so they never appear in your shell
history or the process table:

  # via env (the same vars the live request path reads)
  SLACK_XOXC_TOKEN=xoxc-... SLACK_D_COOKIE=xoxd-... \
    slack-cookie-pp-cli auth set-token --account work --workspace example

  # via a key=value stdin blob
  printf 'xoxc=xoxc-...\nd=xoxd-...\n' | \
    slack-cookie-pp-cli auth set-token --account work --workspace example

--xoxc/--cookie flags are accepted as a fallback but print a warning, since
argv leaks the long-lived secret.`,
		Example: "  slack-cookie-pp-cli auth set-token --account work --workspace example",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Verify-friendly shape: a bare invocation prints help and exits 0;
			// a partial invocation (some flags, missing required) is a usage
			// error (exit 2).
			if cmd.Flags().NFlag() == 0 && len(args) == 0 {
				return cmd.Help()
			}
			if strings.TrimSpace(account) == "" {
				return usageErr(fmt.Errorf("--account is required"))
			}
			if strings.TrimSpace(workspace) == "" {
				return usageErr(fmt.Errorf("--workspace is required"))
			}
			if other := collidingAccount(flags.configPath, account); other != "" {
				return usageErr(fmt.Errorf("account %q would share a data file with existing account %q (the names differ only in punctuation, which is normalized to '_'); choose a distinct name", account, other))
			}

			blob := readSecretBlob(cmd)

			warned := false
			xoxc := resolveSecret(xoxcFlag, "SLACK_XOXC_TOKEN", "xoxc", blob, &warned)
			cookie := resolveSecret(cookieFlag, "SLACK_D_COOKIE", "d", blob, &warned)
			ds := resolveSecret(dsFlag, "SLACK_D_S_COOKIE", "d-s", blob, &warned)

			if warned {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: passing secrets as flag values leaks them into argv; prefer stdin or env vars")
			}
			if xoxc == "" {
				return usageErr(fmt.Errorf("missing xoxc token: provide via $SLACK_XOXC_TOKEN, an 'xoxc=' stdin line, or --xoxc"))
			}
			if cookie == "" {
				return usageErr(fmt.Errorf("missing d cookie: provide via $SLACK_D_COOKIE, a 'd=' stdin line, or --cookie"))
			}

			acct := config.Account{
				Workspace: strings.TrimSpace(workspace),
				Xoxc:      xoxc,
				DCookie:   cookie,
				DSCookie:  ds,
			}
			if err := config.SaveAccount(account, acct, makeDef); err != nil {
				return configErr(fmt.Errorf("saving account: %w", err))
			}

			_, def := accountsFor(flags.configPath)
			path, _ := config.AccountsFilePath()
			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"saved":         true,
					"account":       account,
					"workspace":     acct.Workspace,
					"default":       def,
					"accounts_path": path,
					"xoxc":          maskTail(xoxc),
					"cookie":        maskTail(cookie),
				}, flags)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Account %q saved (workspace %s). Default account: %s.\n", account, acct.Workspace, def)
			return nil
		},
	}
	cmd.Flags().StringVar(&account, "account", "", "Account name to save (required)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace (e.g. example, example.slack.com) (required)")
	cmd.Flags().StringVar(&xoxcFlag, "xoxc", "", "xoxc token (fallback; prefer $SLACK_XOXC_TOKEN or stdin)")
	cmd.Flags().StringVar(&cookieFlag, "cookie", "", "d cookie (fallback; prefer $SLACK_D_COOKIE or stdin)")
	cmd.Flags().StringVar(&dsFlag, "d-s", "", "d-s cookie (optional; prefer $SLACK_D_S_COOKIE or stdin)")
	cmd.Flags().BoolVar(&makeDef, "default", false, "Make this the default account")
	return cmd
}

// workspaceWebURL builds the workspace web host (not the /api host) used for
// the bootstrap-token fetch.
func workspaceWebURL(ws string) string {
	ws = strings.TrimSpace(ws)
	if strings.Contains(ws, "://") {
		// Harden the same way the live request path does: a non-loopback
		// http:// host is upgraded to https:// so the d cookie is never sent
		// in cleartext on the bootstrap GET (config.setWorkspaceHost uses the
		// same helper).
		return config.UpgradeInsecureHost(strings.TrimRight(ws, "/"))
	}
	host := strings.TrimSuffix(ws, "/")
	if !strings.Contains(host, ".") {
		host += ".slack.com"
	}
	return "https://" + host
}

func newSlackcookieAuthLoginCmd(flags *rootFlags) *cobra.Command {
	var (
		account    string
		workspace  string
		cookieFlag string
		dsFlag     string
		makeDef    bool
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with only the d cookie (mints the xoxc token for you)",
		Long: `Authenticate a workspace by supplying just the d cookie; the xoxc token is
minted from the workspace bootstrap. Provide the cookie via stdin or env
($SLACK_D_COOKIE) so it never lands in argv.

If the exchange cannot find a token (the page shape changed, or the cookie is
rotated), supply the xoxc token directly with 'auth set-token' instead.`,
		Example: "  SLACK_D_COOKIE=xoxd-... slack-cookie-pp-cli auth login --account work --workspace example",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().NFlag() == 0 && len(args) == 0 {
				return cmd.Help()
			}
			if strings.TrimSpace(account) == "" {
				return usageErr(fmt.Errorf("--account is required"))
			}
			if strings.TrimSpace(workspace) == "" {
				return usageErr(fmt.Errorf("--workspace is required"))
			}
			if other := collidingAccount(flags.configPath, account); other != "" {
				return usageErr(fmt.Errorf("account %q would share a data file with existing account %q (the names differ only in punctuation, which is normalized to '_'); choose a distinct name", account, other))
			}

			blob := readSecretBlob(cmd)
			warned := false
			cookie := resolveSecret(cookieFlag, "SLACK_D_COOKIE", "d", blob, &warned)
			ds := resolveSecret(dsFlag, "SLACK_D_S_COOKIE", "d-s", blob, &warned)
			if warned {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: passing the cookie as a flag value leaks it into argv; prefer stdin or $SLACK_D_COOKIE")
			}
			if cookie == "" {
				return usageErr(fmt.Errorf("missing d cookie: provide via $SLACK_D_COOKIE, a 'd=' stdin line, or --cookie"))
			}

			ctx, cancel := boundCtx(cmd.Context(), flags)
			defer cancel()
			hc := &http.Client{Timeout: flags.timeout}
			xoxc, err := slackclient.MintXoxcFromCookie(ctx, hc, workspaceWebURL(workspace), cookie, ds)
			if err != nil {
				return authErr(fmt.Errorf("could not mint a token from the cookie (it may be expired or rotated): %w; supply the xoxc token directly with 'auth set-token'", err))
			}

			acct := config.Account{Workspace: strings.TrimSpace(workspace), Xoxc: xoxc, DCookie: cookie, DSCookie: ds}
			if err := config.SaveAccount(account, acct, makeDef); err != nil {
				return configErr(fmt.Errorf("saving account: %w", err))
			}
			_, def := accountsFor(flags.configPath)
			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"logged_in": true,
					"account":   account,
					"workspace": acct.Workspace,
					"default":   def,
					"xoxc":      maskTail(xoxc),
				}, flags)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %q (workspace %s); minted token %s. Default account: %s.\n", account, acct.Workspace, maskTail(xoxc), def)
			return nil
		},
	}
	cmd.Flags().StringVar(&account, "account", "", "Account name to save (required)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace (e.g. example, example.slack.com) (required)")
	cmd.Flags().StringVar(&cookieFlag, "cookie", "", "d cookie (fallback; prefer $SLACK_D_COOKIE or stdin)")
	cmd.Flags().StringVar(&dsFlag, "d-s", "", "d-s cookie (optional; prefer $SLACK_D_S_COOKIE or stdin)")
	cmd.Flags().BoolVar(&makeDef, "default", false, "Make this the default account")
	return cmd
}

func newSlackcookieAuthListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List configured Slack accounts (secrets masked)",
		Example: "  slack-cookie-pp-cli auth list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, def := accountsFor(flags.configPath)
			names := make([]string, 0, len(accounts))
			for n := range accounts {
				names = append(names, n)
			}
			sort.Strings(names)

			if flags.asJSON {
				rows := make([]map[string]any, 0, len(names))
				for _, n := range names {
					a := accounts[n]
					rows = append(rows, map[string]any{
						"account":   n,
						"workspace": a.Workspace,
						"default":   n == def,
						"xoxc":      maskTail(a.Xoxc),
						"cookie":    maskTail(a.DCookie),
						"has_d_s":   strings.TrimSpace(a.DSCookie) != "",
					})
				}
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"accounts":        rows,
					"default_account": def,
				}, flags)
			}

			w := cmd.OutOrStdout()
			if len(names) == 0 {
				fmt.Fprintln(w, "No accounts configured. Add one with 'auth set-token' or 'auth login'.")
				return nil
			}
			headers := []string{"ACCOUNT", "WORKSPACE", "DEFAULT", "XOXC", "COOKIE"}
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				a := accounts[n]
				marker := ""
				if n == def {
					marker = "*"
				}
				rows = append(rows, []string{n, a.Workspace, marker, maskTail(a.Xoxc), maskTail(a.DCookie)})
			}
			return flags.printTable(cmd, headers, rows)
		},
	}
}

func newSlackcookieAuthStatusCmd(flags *rootFlags) *cobra.Command {
	var allAccounts bool
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show authentication status per account",
		Example: "  slack-cookie-pp-cli auth status --account work\n  slack-cookie-pp-cli auth status --all-accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, def := accountsFor(flags.configPath)
			targets := resolveAccountSet(flags.configPath, flags.account, allAccounts)

			type acctStatus struct {
				Account     string `json:"account"`
				Workspace   string `json:"workspace"`
				Default     bool   `json:"default"`
				HasXoxc     bool   `json:"has_xoxc"`
				HasCookie   bool   `json:"has_cookie"`
				Credentials string `json:"credentials"`
			}
			statuses := make([]acctStatus, 0, len(targets))
			for _, name := range targets {
				a, ok := accounts[name]
				st := acctStatus{Account: name, Workspace: a.Workspace, Default: name == def}
				st.HasXoxc = strings.TrimSpace(a.Xoxc) != ""
				st.HasCookie = strings.TrimSpace(a.DCookie) != ""
				switch {
				case !ok:
					st.Credentials = "not configured"
				case st.HasXoxc && st.HasCookie:
					st.Credentials = "present (xoxc + cookie)"
				case st.HasXoxc:
					st.Credentials = "incomplete (xoxc only, no cookie)"
				default:
					st.Credentials = "incomplete"
				}
				statuses = append(statuses, st)
			}

			// Env-var fallback: surface the flat single-account env path so a
			// user driving the CLI via SLACK_XOXC_TOKEN sees they are authed
			// even with no stored accounts.
			envXoxc := strings.TrimSpace(os.Getenv("SLACK_XOXC_TOKEN")) != ""

			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"accounts":     statuses,
					"env_xoxc_set": envXoxc,
				}, flags)
			}
			w := cmd.OutOrStdout()
			if len(accounts) == 0 && !envXoxc {
				fmt.Fprintln(w, red("No accounts configured and no SLACK_XOXC_TOKEN set."))
				fmt.Fprintln(w, "Add one with: slack-cookie-pp-cli auth set-token --account <name> --workspace <ws>")
				return authErr(fmt.Errorf("no credentials configured"))
			}
			for _, st := range statuses {
				marker := ""
				if st.Default {
					marker = " (default)"
				}
				fmt.Fprintf(w, "%s%s: %s [%s]\n", st.Account, marker, st.Credentials, st.Workspace)
			}
			if envXoxc {
				fmt.Fprintln(w, "SLACK_XOXC_TOKEN env var is set (flat single-account path).")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Show status for every configured account")
	return cmd
}

func newSlackcookieAuthLogoutCmd(flags *rootFlags) *cobra.Command {
	var allAccounts bool
	cmd := &cobra.Command{
		Use:     "logout",
		Short:   "Clear stored credentials for an account (or all accounts)",
		Example: "  slack-cookie-pp-cli auth logout --account work\n  slack-cookie-pp-cli auth logout --all-accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			removed := []string{}

			if allAccounts {
				names, _ := listAccounts(flags.configPath)
				for _, n := range names {
					if ok, err := config.DeleteAccount(n); err != nil {
						return configErr(fmt.Errorf("removing account %q: %w", n, err))
					} else if ok {
						removed = append(removed, n)
					}
				}
			} else if strings.TrimSpace(flags.account) != "" {
				ok, err := config.DeleteAccount(flags.account)
				if err != nil {
					return configErr(fmt.Errorf("removing account %q: %w", flags.account, err))
				}
				if !ok {
					return usageErr(fmt.Errorf("account %q not found", flags.account))
				}
				removed = append(removed, flags.account)
			} else {
				// No account specified: clear the legacy single-credential store.
				cfg, err := config.Load(flags.configPath)
				if err != nil {
					return configErr(err)
				}
				if err := cfg.ClearTokens(); err != nil {
					return configErr(fmt.Errorf("clearing tokens: %w", err))
				}
			}

			envStillSet := os.Getenv("SLACK_XOXC_TOKEN") != ""
			if flags.asJSON {
				out := map[string]any{"cleared": true, "removed_accounts": removed}
				if envStillSet {
					out["note"] = "SLACK_XOXC_TOKEN env var is still set"
				}
				return printJSONFiltered(cmd.OutOrStdout(), out, flags)
			}
			w := cmd.OutOrStdout()
			if len(removed) > 0 {
				fmt.Fprintf(w, "Removed account(s): %s\n", strings.Join(removed, ", "))
			} else {
				fmt.Fprintln(w, "Credentials cleared.")
			}
			if envStillSet {
				fmt.Fprintln(w, "Note: SLACK_XOXC_TOKEN env var is still set.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Remove every configured account")
	return cmd
}
