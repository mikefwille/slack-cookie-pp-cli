// Hand-authored extension (survives regen): `missed` — time-windowed personal
// mentions and DMs from the local mirror. Offline, store-only.

package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newMissedCmd(flags *rootFlags) *cobra.Command {
	var allAccounts bool
	var since string
	var me string
	cmd := &cobra.Command{
		Use:         "missed",
		Short:       "Recent mentions of you and DMs since a cutoff (offline)",
		Long:        "List messages that mention you or were sent to you in a DM, newer than --since, from the local mirror. Your user id is taken from --me, or resolved via a single auth.test call when omitted (skipped under --data-source local).",
		Example:     "  slack-cookie-pp-cli missed --since 4h --account work",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cutoff, err := parseSinceCutoff(since)
			if err != nil {
				return usageErr(err)
			}
			ctx, cancel := boundCtx(cmd.Context(), flags)
			defer cancel()

			mirrors, err := openAccountMirrors(ctx, flags, allAccounts)
			if err != nil {
				return err
			}
			defer closeMirrors(mirrors)

			var hits []storedMessage
			var missing []string
			mentionActive := false
			for _, m := range mirrors {
				if m.Missing {
					missing = append(missing, m.Account)
					continue
				}
				// Resolve the user id per account: separate-login workspaces have
				// distinct ids, so one shared id would silently drop the other
				// accounts' mentions under --all-accounts.
				myID := resolveAccountUserID(ctx, flags, m.Account, me)
				mention := ""
				if myID != "" {
					mention = "<@" + myID
					mentionActive = true
				}
				msgs, err := loadMessages(ctx, m.Store, m.Account, cutoff)
				if err != nil {
					return err
				}
				for _, msg := range msgs {
					// Require a closing delimiter ('>' or '|name') so "<@U123"
					// does not also match a longer id like "<@U1234>".
					isMention := mention != "" &&
						(strings.Contains(msg.Text, mention+">") || strings.Contains(msg.Text, mention+"|"))
					isDM := conversationIsDM(ctx, m.Store, msg.Channel)
					if isMention || isDM {
						hits = append(hits, msg)
					}
				}
			}
			sort.Slice(hits, func(i, j int) bool { return hits[i].Time.After(hits[j].Time) })

			if flags.asJSON {
				out := map[string]any{"since": since, "mention_filter_active": mentionActive, "messages": hits, "coverage": mirrorCoverage(mirrors)}
				if len(missing) > 0 {
					out["missing_mirrors"] = missing
				}
				return printJSONFiltered(cmd.OutOrStdout(), out, flags)
			}
			w := cmd.OutOrStdout()
			printMirrorCoverage(cmd.ErrOrStderr(), mirrors)
			if !mentionActive {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: your user id is unknown (pass --me <U…> or authenticate) — showing DMs only, not mentions")
			}
			for _, a := range missing {
				fmt.Fprintf(cmd.ErrOrStderr(), "[%s] %s\n", a, missingMirrorHint(a))
			}
			if len(hits) == 0 {
				fmt.Fprintln(w, "Nothing missed in this window.")
				return nil
			}
			for _, msg := range hits {
				fmt.Fprintf(w, "[%s] %s %s: %s\n", msg.Account, msg.Time.Format("2006-01-02 15:04"), msg.User, oneLine(msg.Text))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Search every configured account")
	cmd.Flags().StringVar(&since, "since", "1d", "Time window (e.g. 4h, 1d, 1w)")
	cmd.Flags().StringVar(&me, "me", "", "Your Slack user id (U…); resolved via auth.test when omitted")
	return cmd
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	// Truncate on rune boundaries so a multi-byte rune (emoji, accented text)
	// is never split into a broken replacement character.
	if r := []rune(s); len(r) > 120 {
		return string(r[:117]) + "..."
	}
	return s
}
