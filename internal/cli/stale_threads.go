// Hand-authored extension (survives regen): `stale-threads` — threads you are
// in (or mentioned in) with no reply for N days. Offline, store-only.

package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newStaleThreadsCmd(flags *rootFlags) *cobra.Command {
	var allAccounts bool
	var days int
	var me string
	cmd := &cobra.Command{
		Use:         "stale-threads",
		Short:       "Threads you're in with no reply for N days (offline)",
		Long:        "From the local mirror, find threads whose last reply is older than --days, scoped to threads you participated in or were mentioned in. Your user id comes from --me, or is resolved per account via a single auth.test when omitted (skipped under --data-source local); if it cannot be resolved, every thread is considered.",
		Example:     "  slack-cookie-pp-cli stale-threads --days 7 --account work",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if days <= 0 {
				return usageErr(fmt.Errorf("--days must be a positive number"))
			}
			cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UTC()
			ctx, cancel := boundCtx(cmd.Context(), flags)
			defer cancel()

			mirrors, err := openAccountMirrors(ctx, flags, allAccounts)
			if err != nil {
				return err
			}
			defer closeMirrors(mirrors)

			type staleThread struct {
				Account   string    `json:"account"`
				ThreadTS  string    `json:"thread_ts"`
				Channel   string    `json:"channel,omitempty"`
				LastReply time.Time `json:"last_reply"`
				Messages  int       `json:"messages"`
			}
			var stale []staleThread
			scanned := 0
			var missing []string

			for _, m := range mirrors {
				if m.Missing {
					missing = append(missing, m.Account)
					continue
				}
				// Resolve the participation id per account (separate logins have
				// distinct ids). Empty id => no participation filter for that
				// account (every thread considered).
				myID := resolveAccountUserID(ctx, flags, m.Account, me)
				mention := ""
				if myID != "" {
					mention = "<@" + myID
				}
				msgs, err := loadMessages(ctx, m.Store, m.Account, time.Time{})
				if err != nil {
					return err
				}
				// Group by thread key (thread_ts, falling back to the message ts
				// for a root that owns replies).
				type agg struct {
					last        time.Time
					count       int
					channel     string
					participant bool
				}
				threads := map[string]*agg{}
				for _, msg := range msgs {
					key := msg.ThreadTS
					if key == "" {
						// Only treat as a thread root when it actually has replies.
						if msg.ReplyCount > 0 {
							key = msg.TS
						} else {
							continue
						}
					}
					a := threads[key]
					if a == nil {
						a = &agg{}
						threads[key] = a
					}
					a.count++
					if msg.Time.After(a.last) {
						a.last = msg.Time
					}
					if msg.Channel != "" {
						a.channel = msg.Channel
					}
					if mention == "" || msg.User == myID ||
						strings.Contains(msg.Text, mention+">") || strings.Contains(msg.Text, mention+"|") {
						a.participant = true
					}
				}
				for key, a := range threads {
					scanned++
					if !a.participant {
						continue
					}
					if a.last.Before(cutoff) {
						stale = append(stale, staleThread{
							Account: m.Account, ThreadTS: key, Channel: a.channel,
							LastReply: a.last, Messages: a.count,
						})
					}
				}
			}
			sort.Slice(stale, func(i, j int) bool { return stale[i].LastReply.Before(stale[j].LastReply) })

			if flags.asJSON {
				out := map[string]any{"days": days, "scanned_threads": scanned, "stale": stale}
				if len(missing) > 0 {
					out["missing_mirrors"] = missing
				}
				return printJSONFiltered(cmd.OutOrStdout(), out, flags)
			}
			w := cmd.OutOrStdout()
			for _, a := range missing {
				fmt.Fprintf(cmd.ErrOrStderr(), "[%s] %s\n", a, missingMirrorHint(a))
			}
			if len(stale) == 0 {
				fmt.Fprintf(w, "No stale threads (scanned %d). Widen with a smaller --days, or sync more history.\n", scanned)
				return nil
			}
			for _, s := range stale {
				ch := s.Channel
				if ch == "" {
					ch = "(unknown channel)"
				}
				fmt.Fprintf(w, "[%s] thread %s in %s — last reply %s (%d msgs)\n",
					s.Account, s.ThreadTS, ch, s.LastReply.Format("2006-01-02"), s.Messages)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Scan every configured account")
	cmd.Flags().IntVar(&days, "days", 7, "Staleness threshold in days")
	cmd.Flags().StringVar(&me, "me", "", "Your Slack user id (U…) to scope to your participation; omit to consider all threads")
	return cmd
}
