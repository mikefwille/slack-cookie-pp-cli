// Hand-authored extension (survives regen): the `ai` command family — native
// Slack AI access from the terminal.
//
//   ai ask "<q>"    headline; rides documented chat.postMessage + replies poll
//   ai search "<q>" search.inline inline AI answer (undocumented, degrades)
//   ai recap        ai.alpha.digest.* + ai ask fallback (undocumented, degrades)
//   ai skills       ai.alpha.slackbot.skills.listEnabled (undocumented, degrades)
//
// The undocumented endpoints are isolated in internal/slackclient so a
// Slack-side change to any of them can only ever degrade `ai search/recap/skills`
// to a clean "not available here" notice, never break unrelated commands.

package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"slack-cookie-pp-cli/internal/cliutil"
	"slack-cookie-pp-cli/internal/slackclient"
)

func newAICmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "Ask Slack's AI and Slackbot assistant from the terminal",
		RunE:  parentNoSubcommandRunE(flags),
	}
	cmd.AddCommand(newAIAskCmd(flags))
	cmd.AddCommand(newAISearchCmd(flags))
	cmd.AddCommand(newAIRecapCmd(flags))
	cmd.AddCommand(newAISkillsCmd(flags))
	return cmd
}

func newAIAskCmd(flags *rootFlags) *cobra.Command {
	var wait time.Duration
	var pollInterval time.Duration
	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask the Slackbot AI assistant a question and print its answer",
		Long: `Post a question to the Slackbot AI assistant and poll its threaded reply
until the answer settles. Uses only documented Slack methods (chat.postMessage
+ conversations.replies), so it is the most robust AI command.

This posts a real message into your Slackbot DM, so it is NOT a read-only
operation. Use --dry-run to preview without sending.`,
		Example: "  slack-cookie-pp-cli ai ask \"what did I miss in #general today?\"",
		Args:    cobra.ArbitraryArgs,
		// ai ask posts a message; not mcp:read-only.
		Annotations: map[string]string{"pp:method": "POST", "pp:path": "/chat.postMessage"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().NFlag() == 0 && len(args) == 0 && !flags.dryRun {
				return cmd.Help()
			}
			question := strings.TrimSpace(strings.Join(args, " "))
			if question == "" {
				return usageErr(fmt.Errorf("a question is required: ai ask \"<question>\""))
			}

			if flags.dryRun {
				fmt.Fprintf(cmd.ErrOrStderr(), "POST /chat.postMessage (Slackbot IM)\n  text: %s\n\n(dry run - no message sent)\n", question)
				if flags.asJSON {
					return printJSONFiltered(cmd.OutOrStdout(), map[string]any{"dry_run": true, "question": question}, flags)
				}
				return nil
			}

			// Verify-mode short-circuit mirrors the client's mutating-verb gate:
			// under PRINTING_PRESS_VERIFY=1 without LIVE_HTTP=1, never post.
			if cliutil.IsVerifyEnv() && !cliutil.IsVerifyLiveHTTPEnv() {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"status": "noop", "reason": "verify_short_circuit", "question": question,
				}, flags)
			}

			c, err := flags.newClient()
			if err != nil {
				return err
			}
			ai := slackclient.New(c)
			res, err := ai.Ask(cmd.Context(), question, slackclient.AskOptions{
				Timeout:      wait,
				PollInterval: pollInterval,
			})
			if err != nil {
				return classifyAPIError(err, flags)
			}

			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), res, flags)
			}
			w := cmd.OutOrStdout()
			if strings.TrimSpace(res.Answer) == "" {
				fmt.Fprintln(w, "No answer yet — the assistant did not reply before the wait elapsed.")
				fmt.Fprintln(w, "Try a longer --wait, or check that Slack AI / Slackbot is enabled for this workspace.")
				return nil
			}
			fmt.Fprintln(w, res.Answer)
			if res.TimedOut {
				fmt.Fprintln(cmd.ErrOrStderr(), "\n(note: reply may be incomplete — wait elapsed before it settled; increase --wait)")
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 90*time.Second, "Max time to wait for the assistant's reply to settle")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 2*time.Second, "Interval between reply polls")
	return cmd
}

func newAISearchCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "search <query>",
		Short:       "Show Slack's inline AI answer (search.inline) for a query",
		Long:        "Surface Slack's inline AI answer card via the undocumented search.inline endpoint. Degrades to a clean 'not available' notice when the endpoint is unavailable for this workspace.",
		Example:     "  slack-cookie-pp-cli ai search \"nps decisions\"",
		Args:        cobra.ArbitraryArgs,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().NFlag() == 0 && len(args) == 0 {
				return cmd.Help()
			}
			query := strings.TrimSpace(strings.Join(args, " "))
			if query == "" {
				return usageErr(fmt.Errorf("a query is required: ai search \"<query>\""))
			}
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			res, err := slackclient.New(c).SearchInline(cmd.Context(), query)
			if err != nil {
				return classifyAPIError(err, flags)
			}
			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), res, flags)
			}
			w := cmd.OutOrStdout()
			if !res.Available {
				fmt.Fprintln(w, "Slack AI inline answer not available for this workspace/query.")
				return nil
			}
			if strings.TrimSpace(res.Answer) == "" {
				fmt.Fprintln(w, "Slack returned an inline answer with no extractable text (use --json to see the raw payload).")
				return nil
			}
			fmt.Fprintln(w, res.Answer)
			return nil
		},
	}
	return cmd
}

func newAIRecapCmd(flags *rootFlags) *cobra.Command {
	var since string
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "recap",
		Short: "Recap what you missed (native digest with ai ask fallback)",
		Long: `Summarize recent activity. Tries the undocumented native digest endpoints and
falls back to routing 'what did I miss' through the Slackbot assistant (ai ask)
when they are unavailable.

Because that fallback POSTs a message into your Slackbot DM (via chat.postMessage),
this command is NOT mcp:read-only — it can have a side effect. Use --dry-run to
suppress the posting fallback.`,
		Example: "  slack-cookie-pp-cli ai recap --since 1d",
		Args:    cobra.NoArgs,
		// Not mcp:read-only: the ai-ask fallback posts a message (see Long).
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			ai := slackclient.New(c)

			digest, _ := ai.DigestChannels(cmd.Context())

			window := strings.TrimSpace(since)
			if window == "" {
				window = "1d"
			}

			// Verify short-circuit: ai ask posts, so don't drive the fallback under verify.
			askAvailable := !(cliutil.IsVerifyEnv() && !cliutil.IsVerifyLiveHTTPEnv())
			var ask slackclient.AskResult
			if askAvailable && !flags.dryRun {
				var askErr error
				ask, askErr = ai.Ask(cmd.Context(), fmt.Sprintf("What are the most important things I missed in the last %s? Give me a prioritized recap.", window), slackclient.AskOptions{Timeout: wait})
				// Propagate cancellation/deadline (Ctrl-C, context timeout) instead
				// of swallowing it into the generic "no recap" message; a non-context
				// Ask failure still degrades gracefully below.
				if askErr != nil && cmd.Context().Err() != nil {
					return cmd.Context().Err()
				}
			}

			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), map[string]any{
					"since":            window,
					"digest_available": digest.Available,
					"digest_channels":  digest.ChannelIDs,
					"recap":            ask.Answer,
				}, flags)
			}
			w := cmd.OutOrStdout()
			if digest.Available && len(digest.ChannelIDs) > 0 {
				fmt.Fprintf(w, "%d channel(s) have a native digest ready.\n\n", len(digest.ChannelIDs))
			}
			if strings.TrimSpace(ask.Answer) != "" {
				fmt.Fprintln(w, ask.Answer)
				return nil
			}
			fmt.Fprintln(w, "No recap available. Native digest endpoints returned nothing usable and the assistant did not reply.")
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "1d", "Time window to recap (e.g. 1d, 4h, 1w)")
	cmd.Flags().DurationVar(&wait, "wait", 90*time.Second, "Max time to wait for the assistant fallback reply")
	return cmd
}

func newAISkillsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "skills",
		Short:       "List the Slackbot assistant's enabled skills",
		Long:        "List the Slackbot AI assistant's enabled skills via the undocumented ai.alpha.slackbot.skills.listEnabled endpoint. Degrades cleanly when unavailable.",
		Example:     "  slack-cookie-pp-cli ai skills",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"mcp:read-only": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := flags.newClient()
			if err != nil {
				return err
			}
			res, err := slackclient.New(c).Skills(cmd.Context())
			if err != nil {
				return classifyAPIError(err, flags)
			}
			if flags.asJSON {
				return printJSONFiltered(cmd.OutOrStdout(), res, flags)
			}
			w := cmd.OutOrStdout()
			if !res.Available {
				fmt.Fprintln(w, "Slackbot skills listing not available for this workspace.")
				return nil
			}
			if len(res.Skills) == 0 {
				fmt.Fprintln(w, "No enabled skills reported (use --json to see the raw payload).")
				return nil
			}
			for _, s := range res.Skills {
				fmt.Fprintf(w, "- %s\n", s)
			}
			return nil
		},
	}
	return cmd
}
