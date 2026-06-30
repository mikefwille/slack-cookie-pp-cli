// Hand-authored extension (survives regen): the Slack-specific per-account
// auth.test verdict that the generated doctor delegates to.
//
// The generated doctor probes /auth.test only for reachability. This adds the
// identity verdict — ok/team/user per account — with invalidation-aware
// messaging, and supports --account / --all-accounts. It overrides
// report["credentials"] (already rendered + fed to the exit logic) so no
// change to the generated render loop is needed, and feeds doctorExitWithAuth
// so a definitively invalid/rotated session exits non-zero.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/cliutil"
	"slack-cookie-pp-cli/internal/config"
)

// authVerdict is one account's auth.test outcome.
type authVerdict struct {
	Account string `json:"account"`
	OK      bool   `json:"ok"`
	Team    string `json:"team,omitempty"`
	User    string `json:"user,omitempty"`
	Verdict string `json:"verdict"`
	invalid bool   // definitively invalid/rotated session (drives non-zero exit)
}

// slackcookieDoctorAuth runs an auth.test probe per targeted account and folds
// the result into report. It is a no-op under verify mode (the mock server
// would yield false verdicts). Called once from the generated doctor RunE.
func slackcookieDoctorAuth(cmd *cobra.Command, flags *rootFlags, report map[string]any) {
	if cliutil.IsVerifyEnv() {
		return
	}
	allAccounts := false
	if f := cmd.Flags().Lookup("all-accounts"); f != nil {
		allAccounts = f.Value.String() == "true"
	}
	targets := resolveAccountSet(flags.configPath, flags.account, allAccounts)

	ctx, cancel := boundCtx(cmd.Context(), flags)
	defer cancel()

	verdicts := make([]authVerdict, 0, len(targets))
	for _, name := range targets {
		verdicts = append(verdicts, probeAccountAuth(ctx, flags, name))
	}

	report["accounts"] = verdicts
	switch len(verdicts) {
	case 0:
		// nothing to add
	case 1:
		report["credentials"] = verdicts[0].Verdict
	default:
		report["credentials"] = summarizeAuthVerdicts(verdicts)
	}
}

func probeAccountAuth(ctx context.Context, flags *rootFlags, account string) authVerdict {
	v := authVerdict{Account: account}
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		v.Verdict = fmt.Sprintf("ERROR config load failed for %q: %v", account, err)
		return v
	}
	config.ApplyAccount(cfg, account)
	if cfg.AuthHeader() == "" {
		v.Verdict = fmt.Sprintf("%s: skipped (no credentials configured)", account)
		return v
	}
	c := client.New(cfg, flags.timeout, flags.rateLimit)
	c.NoCache = true // doctor must see fresh auth state, not a 5-min cached verdict

	raw, err := c.GetNoCache(ctx, "/auth.test", nil)
	if err != nil {
		// A reachable-but-rejecting API (HTTP 401/403) is a definitive invalid
		// session; an unreachable API is not the credential's fault.
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			v.invalid = true
			v.Verdict = fmt.Sprintf("ERROR %s: session invalid or rotated (HTTP %d) — re-run auth login", account, apiErr.StatusCode)
			return v
		}
		v.Verdict = fmt.Sprintf("%s: skipped (API unreachable: %v)", account, err)
		return v
	}

	var body struct {
		OK    bool   `json:"ok"`
		Team  string `json:"team"`
		User  string `json:"user"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	switch {
	case body.OK:
		v.OK, v.Team, v.User = true, body.Team, body.User
		v.Verdict = fmt.Sprintf("authenticated as %s in %s", body.User, body.Team)
	case body.Error == "invalid_auth" || body.Error == "not_authed" || body.Error == "token_revoked" || body.Error == "account_inactive":
		v.invalid = true
		v.Verdict = fmt.Sprintf("ERROR %s: session invalid or rotated (%s) — re-run auth login", account, body.Error)
	default:
		errLabel := body.Error
		if errLabel == "" {
			errLabel = "ok:false"
		}
		v.Verdict = fmt.Sprintf("ERROR %s: auth.test failed (%s)", account, errLabel)
	}
	return v
}

func summarizeAuthVerdicts(verdicts []authVerdict) string {
	authed := 0
	for _, v := range verdicts {
		if v.OK {
			authed++
		}
	}
	if authed == len(verdicts) {
		return fmt.Sprintf("authenticated: %d/%d accounts", authed, len(verdicts))
	}
	return fmt.Sprintf("ERROR %d/%d accounts authenticated; others invalid or unreachable", authed, len(verdicts))
}

// doctorExitWithAuth forces a non-zero exit when any probed account has a
// definitively invalid/rotated session, regardless of --fail-on; otherwise it
// preserves the generated --fail-on gating. The generated doctor RunE calls
// this in place of doctorExitForFailOn (a recorded one-token edit).
func doctorExitWithAuth(failOn string, report map[string]any) error {
	if verdicts, ok := report["accounts"].([]authVerdict); ok {
		for _, v := range verdicts {
			if v.invalid {
				return authErr(fmt.Errorf("doctor: account %q has an invalid or rotated session", v.Account))
			}
		}
	}
	return doctorExitForFailOn(failOn, report)
}
