package cli

import (
	"fmt"
	"github.com/spf13/cobra"
	"slack-cookie-pp-cli/internal/client"
	"slack-cookie-pp-cli/internal/slackmirror"
)

func runSlackMirrorSync(cmd *cobra.Command, flags *rootFlags, c *client.Client, dbPath string, resources []string, since string, full, latestOnly, strict bool, maxPages int, userParams *syncUserParams) error {
	if err := userParams.validateResourceNames(knownSyncResourceNames()); err != nil {
		return usageErr(err)
	}
	if maxPages < 0 {
		return usageErr(fmt.Errorf("--max-pages must be nonnegative"))
	}
	if flags.dataSource == "local" {
		return usageErr(fmt.Errorf("sync requires live source reads"))
	}
	if latestOnly {
		maxPages = 1
	}
	oldest := ""
	if since != "" {
		t, err := parseSinceDuration(since)
		if err != nil {
			return usageErr(err)
		}
		oldest = slackmirror.Stamp(t)
	}
	params := map[string]map[string]string{}
	for _, name := range resources {
		p := map[string]string{}
		userParams.applyTo(name, p, false)
		params[name] = p
	}
	if params["conversations"] == nil {
		p := map[string]string{}
		userParams.applyTo("conversations", p, false)
		params["conversations"] = p
	}
	r, err := slackmirror.Open(cmd.Context(), c, dbPath)
	if err != nil {
		return err
	}
	defer r.Store.Close()
	report, err := r.Archive(cmd.Context(), slackmirror.ArchiveOptions{Resources: resources, Full: full, Since: since, Oldest: oldest, MaxPages: maxPages, Params: params})
	if printErr := printJSONFiltered(cmd.OutOrStdout(), map[string]any{"event": "sync_summary", "result": report}, flags); printErr != nil {
		return printErr
	}
	if err != nil {
		return err
	}
	if strict && report.Coverage.State != "complete" {
		return fmt.Errorf("sync incomplete; inspect coverage reasons and rerun to resume")
	}
	return nil
}
