// Hand-authored extension (survives regen): `trends` — per-channel message
// volume by ISO week from the local mirror. Offline, store-only. Messages
// without channel context (most history rows) bucket under "(unknown)".

package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

func newTrendsCmd(flags *rootFlags) *cobra.Command {
	var allAccounts bool
	var weeks int
	cmd := &cobra.Command{
		Use:         "trends",
		Short:       "Per-channel message volume by week (offline)",
		Long:        "Aggregate message counts per channel per ISO week over the last --weeks from the local mirror. Channel context is only present for search-sourced messages; history rows without it bucket under \"(unknown)\".",
		Example:     "  slack-cookie-pp-cli trends --weeks 4 --account work",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if weeks <= 0 {
				return usageErr(fmt.Errorf("--weeks must be a positive number"))
			}
			cutoff := time.Now().Add(-time.Duration(weeks) * 7 * 24 * time.Hour).UTC()
			ctx, cancel := boundCtx(cmd.Context(), flags)
			defer cancel()

			mirrors, err := openAccountMirrors(ctx, flags, allAccounts)
			if err != nil {
				return err
			}
			defer closeMirrors(mirrors)

			// counts[channel][weekLabel] = n
			counts := map[string]map[string]int{}
			weekSet := map[string]struct{}{}
			var missing []string
			total := 0
			for _, m := range mirrors {
				if m.Missing {
					missing = append(missing, m.Account)
					continue
				}
				msgs, err := loadMessages(ctx, m.Store, m.Account, cutoff)
				if err != nil {
					return err
				}
				for _, msg := range msgs {
					if msg.Time.IsZero() {
						continue
					}
					ch := msg.Channel
					if ch == "" {
						ch = "(unknown)"
					}
					label := isoWeekLabel(msg.Time)
					if counts[ch] == nil {
						counts[ch] = map[string]int{}
					}
					counts[ch][label]++
					weekSet[label] = struct{}{}
					total++
				}
			}

			weekLabels := make([]string, 0, len(weekSet))
			for w := range weekSet {
				weekLabels = append(weekLabels, w)
			}
			sort.Strings(weekLabels)
			channels := make([]string, 0, len(counts))
			for c := range counts {
				channels = append(channels, c)
			}
			sort.Strings(channels)

			if flags.asJSON {
				series := make([]map[string]any, 0, len(channels))
				for _, ch := range channels {
					series = append(series, map[string]any{"channel": ch, "weeks": counts[ch]})
				}
				out := map[string]any{"weeks": weekLabels, "series": series, "total": total}
				if len(missing) > 0 {
					out["missing_mirrors"] = missing
				}
				out["coverage"] = mirrorCoverage(mirrors)
				return printJSONFiltered(cmd.OutOrStdout(), out, flags)
			}

			w := cmd.OutOrStdout()
			printMirrorCoverage(cmd.ErrOrStderr(), mirrors)
			for _, a := range missing {
				fmt.Fprintf(cmd.ErrOrStderr(), "[%s] %s\n", a, missingMirrorHint(a))
			}
			if total == 0 {
				fmt.Fprintln(w, "No messages in the local mirror for this window. Run a sync or read commands first.")
				return nil
			}
			headers := append([]string{"CHANNEL"}, weekLabels...)
			rows := make([][]string, 0, len(channels))
			for _, ch := range channels {
				row := []string{ch}
				for _, wl := range weekLabels {
					row = append(row, fmt.Sprintf("%d", counts[ch][wl]))
				}
				rows = append(rows, row)
			}
			return flags.printTable(cmd, headers, rows)
		},
	}
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Aggregate every configured account")
	cmd.Flags().IntVar(&weeks, "weeks", 4, "Number of trailing weeks to chart")
	return cmd
}

func isoWeekLabel(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}
