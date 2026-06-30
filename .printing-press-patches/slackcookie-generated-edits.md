# Generated-file edits — reprint guard

These are the hand edits to generated (`DO NOT EDIT`) files made while building the
remaining slack-cookie features (multi-account auth, AI, offline). A
`generate --force` overwrites these files; re-apply each edit below. All durable
*logic* lives in sibling files (`slackcookie_*.go`, `internal/slackclient/*`,
`internal/store/slackcookie_overrides.go`) that survive regen on their own — only
these thin hooks must be re-applied.

## internal/cli/root.go
In `newRootCmd`, immediately before `return rootCmd`, add the single wiring hook:

```go
	addSlackcookieCommands(rootCmd, flags)
```

This registers every novel command and installs the per-account DB resolver.
All wiring logic lives in `internal/cli/slackcookie_commands.go` (sibling).

## internal/cli/helpers.go
In `defaultDBPath`, consult the per-account resolver before the default path:

```go
func defaultDBPath(name string) string {
	if accountDBPathResolver != nil {
		if p := accountDBPathResolver(name); p != "" {
			return p
		}
	}
	...unchanged...
}
```

`accountDBPathResolver` is declared/installed in `slackcookie_accounts.go`.

## internal/cli/doctor.go
Three edits, all delegating to `slackcookie_doctor.go`:
1. Before `report["cache"] = collectCacheReport(...)`, add:
   `slackcookieDoctorAuth(cmd, flags, report)`
2. Replace both `return doctorExitForFailOn(failOn, report)` with
   `return doctorExitWithAuth(failOn, report)`.
3. After the `--fail-on` flag declaration, add:
   `cmd.Flags().Bool("all-accounts", false, "Report auth.test identity for every configured account")`

## internal/cli/conversations_history.go
Change the write-through/read resource type from `"conversations"` to
`"conversations-conversations-history"` in the `resolvePaginatedReadWithStrategy`
call, so history messages (keyed on `ts`) store under their own resource type
instead of polluting the channels table and failing id extraction.

## internal/cli/conversations_replies.go
Change the resource type from `"conversations"` to
`"conversations-conversations-replies"` in the `resolveReadWithStrategyAndResponsePath`
call (same reason as history).

## spec.yaml (authored spec, not generated, but profiler-consumed)
Added `x-resource-id: ts` to `conversations.history`, `conversations.replies`, and
the `search.messages` operations so a regen templates the `ts` override into
`internal/store/store.go`'s `resourceIDFieldOverrides` (the sibling
`slackcookie_overrides.go` carries the same fix at runtime without a regen).
