// Hand-authored extension (survives regen): the single seam that wires every
// novel multi-account / AI / offline command onto the generated command tree.
//
// newRootCmd() calls addSlackcookieCommands(rootCmd, flags) exactly once (a
// one-line edit to the generated root.go, recorded under
// .printing-press-patches/). All actual registration lives here and in the
// per-command sibling files, so a `generate --force` preserves the novel
// surface and only the single hook line must be re-applied.

package cli

import "github.com/spf13/cobra"

func addSlackcookieCommands(rootCmd *cobra.Command, flags *rootFlags) {
	// Route every defaultDBPath call site to the active account's per-account DB.
	installAccountDBResolver(flags)
	wireSlackcookieAuth(rootCmd, flags)
	rootCmd.AddCommand(newAICmd(flags))           // U6-U8
	rootCmd.AddCommand(newDigestCmd(flags))       // U9
	rootCmd.AddCommand(newMissedCmd(flags))       // U10
	rootCmd.AddCommand(newStaleThreadsCmd(flags)) // U11
	rootCmd.AddCommand(newTrendsCmd(flags))       // U12
	rootCmd.AddCommand(newSlackSQLCmd(flags))     // U13
}

// wireSlackcookieAuth replaces the generated single-credential auth
// subcommands (set-token/status/logout) with account-aware versions and adds
// the new `auth list` and `auth login`.
func wireSlackcookieAuth(rootCmd *cobra.Command, flags *rootFlags) {
	auth := findSubcommand(rootCmd, "auth")
	if auth == nil {
		return
	}
	for _, name := range []string{"set-token", "status", "logout"} {
		if c := findSubcommand(auth, name); c != nil {
			auth.RemoveCommand(c)
		}
	}
	auth.AddCommand(newSlackcookieAuthSetTokenCmd(flags))
	auth.AddCommand(newSlackcookieAuthStatusCmd(flags))
	auth.AddCommand(newSlackcookieAuthListCmd(flags))
	auth.AddCommand(newSlackcookieAuthLoginCmd(flags)) // U2
	auth.AddCommand(newSlackcookieAuthLogoutCmd(flags))
}

// findSubcommand returns the direct child command named name, or nil.
func findSubcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
