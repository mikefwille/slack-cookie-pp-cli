// Hand-authored extension (survives regen): `digest` — cross-workspace
// catch-up rollup from the per-account local mirrors. Offline, store-only,
// rate-limit-safe.

package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDigestCmd(flags *rootFlags) *cobra.Command {
	var allAccounts bool
	var since string
	cmd := &cobra.Command{
		Use:         "digest",
		Short:       "Catch-up rollup of recent activity across your workspaces (offline)",
		Long:        "Summarize recent activity from the local per-account mirrors. With --all-accounts it merges every configured workspace into one account-tagged rollup. Reads only the local store; run a sync or read commands first to populate it.",
		Example:     "  slack-cookie-pp-cli digest --all-accounts --since 1d",
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

			type section struct {
				Account  string `json:"account"`
				Missing  bool   `json:"missing,omitempty"`
				Hint     string `json:"hint,omitempty"`
				Messages int    `json:"messages"`
				Users    int    `json:"active_users"`
				Channels int    `json:"channels"`
				Threads  int    `json:"threads"`
			}
			sections := make([]section, 0, len(mirrors))
			for _, m := range mirrors {
				if m.Missing {
					sections = append(sections, section{Account: m.Account, Missing: true, Hint: missingMirrorHint(m.Account)})
					continue
				}
				msgs, err := loadMessages(ctx, m.Store, m.Account, cutoff)
				if err != nil {
					return err
				}
				users, channels, threads := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
				for _, msg := range msgs {
					if msg.User != "" {
						users[msg.User] = struct{}{}
					}
					if msg.Channel != "" {
						channels[msg.Channel] = struct{}{}
					}
					if msg.ThreadTS != "" {
						threads[msg.WorkspaceID+"/"+msg.Channel+"/"+msg.ThreadTS] = struct{}{}
					}
				}
				sections = append(sections, section{
					Account: m.Account, Messages: len(msgs),
					Users: len(users), Channels: len(channels), Threads: len(threads),
				})
			}

			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{"since": since, "accounts": sections, "coverage": mirrorCoverage(mirrors)}, flags)
			}
			w := cmd.OutOrStdout()
			printMirrorCoverage(cmd.ErrOrStderr(), mirrors)
			for _, s := range sections {
				if s.Missing {
					fmt.Fprintf(w, "[%s] %s\n", s.Account, s.Hint)
					continue
				}
				fmt.Fprintf(w, "[%s] %d messages, %d active users, %d channels, %d threads (since %s)\n",
					s.Account, s.Messages, s.Users, s.Channels, s.Threads, sinceLabel(since))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Roll up every configured account")
	cmd.Flags().StringVar(&since, "since", "1d", "Time window (e.g. 1d, 4h, 1w)")
	return cmd
}

func sinceLabel(since string) string {
	if since == "" {
		return "all time"
	}
	return since
}
