package cli

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"slack-cookie-pp-cli/internal/slackmirror"
	"slack-cookie-pp-cli/internal/store"
	"strconv"
)

func configureSlackSearch(cmd *cobra.Command, flags *rootFlags, resourceType *string, limit *int, dbPath *string) {
	var channel, oldest, latest, thread, cursor string
	var all, inclusive bool
	cmd.Flags().StringVar(&channel, "channel", "", "Local conversation ID filter")
	cmd.Flags().StringVar(&oldest, "oldest", "", "Local exclusive lower Slack timestamp")
	cmd.Flags().StringVar(&latest, "latest", "", "Local exclusive upper Slack timestamp")
	cmd.Flags().StringVar(&thread, "thread-ts", "", "Local thread root timestamp")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Local continuation token")
	cmd.Flags().BoolVar(&all, "all", false, "Return all scoped local matches")
	cmd.Flags().BoolVar(&inclusive, "inclusive", false, "Include local timestamp bounds")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		p := map[string]string{"query": args[0], "limit": strconv.Itoa(*limit), "channel": channel, "oldest": oldest, "latest": latest, "ts": thread, "cursor": cursor, "inclusive": strconv.FormatBool(inclusive)}
		if *limit < 1 {
			return usageErr(fmt.Errorf("--limit must be positive"))
		}
		if flags.dataSource != "local" {
			if channel != "" || oldest != "" || latest != "" || thread != "" || cursor != "" || all {
				return usageErr(fmt.Errorf("explicit local search filters require --data-source local"))
			}
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			data, prov, err := ReadSlackCollection(cmd.Context(), c, defaultDBPath("slack-cookie-pp-cli"), "/search.messages", map[string]string{"query": args[0], "count": strconv.Itoa(*limit)}, flags.dataSource, false)
			if err != nil {
				return classifyAPIError(err, flags)
			}
			return printSlackSearch(cmd, flags, data, prov)
		}
		path := *dbPath
		if path == "" {
			path = defaultDBPath("slack-cookie-pp-cli")
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("local mirror unavailable; run sync --resources conversations,messages: %w", err)
		}
		s, err := store.OpenReadOnlyContext(cmd.Context(), path)
		if err != nil {
			return err
		}
		defer s.Close()
		if *resourceType != "" && *resourceType != "messages" {
			return usageErr(fmt.Errorf("canonical local message search supports --type messages; use sql for other resource tables"))
		}
		result, err := slackmirror.Local(cmd.Context(), s, "/local.search", p, all)
		if err != nil {
			return err
		}
		data, prov, err := slackResultData(result)
		if err != nil {
			return err
		}
		return printSlackSearch(cmd, flags, data, prov)
	}
}

func printSlackSearch(cmd *cobra.Command, flags *rootFlags, data []byte, prov DataProvenance) error {
	if flags.csv || flags.plain || flags.quiet {
		return printOutputWithFlags(cmd.OutOrStdout(), data, flags)
	}
	if flags.selectFields != "" {
		data = filterFields(data, flags.selectFields)
	} else if flags.compact {
		data = compactFields(data)
	}
	wrapped, err := wrapWithProvenance(data, prov)
	if err != nil {
		return err
	}
	return printOutput(cmd.OutOrStdout(), wrapped, true)
}
