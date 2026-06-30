# slack-cookie CLI build spec

This is the requirements brief the Printing Press generator will build from. It is not the OpenAPI/YAML spec yet (that gets produced during research/generate). Review and edit anything here before I start. Anything you change, I fold into the build.

## Goal

A token-efficient, agent-native Slack CLI that authenticates as you using your existing logged-in browser session (the `xoxc-` session token plus the `d` cookie), not an OAuth app token. No Slack app creation, no OAuth scopes, no admin approval. It acts fully as you, across all the workspaces you are logged into.

Name: slack-cookie. Binary: slack-cookie-pp-cli. Companion skill: pp-slack-cookie. Library slug: slack-cookie.

## Locked decisions

- Multi-account: separate logins, so a `d` cookie per account (not a shared cookie).
- Slack AI: native Slack AI (interpretation A). At least one workspace has the paid Slack AI add-on. Requires a browser-sniff discovery pass.
- Sending: defaults to `--dry-run`; an explicit flag is required to actually post.
- Message search (search.messages): in scope as a must-have.

## Auth model

The key idea: the `xoxc`/`d` pair authenticates the same Slack Web API methods that an official token does. So the endpoint surface is the documented Slack Web API; only the auth is different.

How a request is made:
- Endpoint base: `https://<workspace>.slack.com/api/<method>` (workspace-scoped host, e.g. example.slack.com), not `https://slack.com/api`.
- The `xoxc-` token is sent as the `token` form field (multipart/form-encoded body), the way the web client does it.
- The `d` cookie is sent as a `Cookie: d=<value>` header. Some workspaces also need the `d-s` cookie; the CLI supports an optional second cookie.
- Method surface: chat.postMessage, conversations.list/history/replies/open, search.messages, users.list/info, etc. Same methods, session auth.

How the `d` cookie and `xoxc` token work for you:
- Your workspaces are separate logins, so each account has its own `d` cookie and its own `xoxc-` token, on its own subdomain.

Fragility note (documented for the user, not a blocker): the `xoxc`/`d` pair expires and rotates, so it needs periodic re-grab from the browser. The `doctor` command reports auth validity clearly, per account.

## Multi-account / multi-workspace

First-class concept. An "account" is a named set of `{ workspace-subdomain, xoxc-token, d-cookie }`. Built for separate logins (a `d` per account).

Config (`config.toml`):

```
default_account = "work"

[accounts.work]
workspace = "example"          # -> example.slack.com
xoxc = "xoxc-..."           # or via env
d_cookie = "..."            # this login's d cookie

[accounts.personal]
workspace = "myteam"        # -> myteam.slack.com
xoxc = "xoxc-..."
d_cookie = "..."            # separate login -> its own d cookie
```

Selection and overrides:
- Global flag `--account <name>` selects which account a command runs against.
- Falls back to `SLACK_ACCOUNT` env, then `default_account`.
- Active-account env overrides: `SLACK_XOXC_TOKEN`, `SLACK_D_COOKIE`, `SLACK_D_S_COOKIE`, `SLACK_WORKSPACE`.
- `--all-accounts` runs read/aggregate commands across every configured account.

Auth management:
- `auth set-token --account work --workspace example --xoxc <...> --cookie <...>` (and optional `--d-s`).
- `auth list` shows configured accounts and validity.
- `auth status [--account <name>]` checks one or all.

Local store namespacing (designed in now so cross-workspace queries work later):
- One SQLite DB file per account (e.g. `~/.local/share/slack-cookie/<account>.db`).
- `sync` writes to the active account's DB. Cross-account commands read across all per-account DBs.

## Capabilities (table stakes to absorb)

1. Send messages as me (chat.postMessage), to channels and DMs, with threading. Default dry-run.
2. Search messages and conversations (search.messages), with filters.
3. List and read channels, DMs, and group DMs (conversations.list/history/replies).
4. Read users / who's who (users.list/info), resolve names.
5. Monitor / digest channel activity (recent activity rollups).
6. Offline local sync to SQLite for fast, repeated, token-cheap queries.

## Slack AI access (native, in scope)

Goal: ask Slack's AI a question from the CLI and get its answer out (text + JSON), plus summaries and recaps.

- Feasible because we auth as the web client: the same internal web API methods the browser uses for Slack AI are reachable with `xoxc` + `d`.
- The exact internal method names and payloads are undocumented. Discovery step: browser-sniff the paid-add-on workspace (trigger an Ask + a thread summary + a channel recap while capturing traffic) to read off the real endpoints and request shapes. Uses the logged-in Chrome session (chrome extension) or agent-browser.
- Gated on the workspace having the paid Slack AI add-on. Commands degrade gracefully with a clear "Slack AI not enabled here" on workspaces without it.
- Draft commands:
  - `ai ask "<question>"` (synthesized answer + citations)
  - `ai summarize <thread-url|channel>`
  - `ai recap <channel> --since <when>`

## Transcendence ideas (only possible with local SQLite + agent-native shape)

Refined and scored in the absorb gate before any are committed. Draft set:
- Cross-workspace digest: one command that rolls up unreads / recent activity across all accounts from synced data ("what did I miss across all my Slacks").
- Stale-thread finder: threads that mention me or that I am in with no reply in X time.
- Activity trends: per-channel message volume by week from sync snapshots.
- "What did I miss": time-windowed personal mentions + DMs since a timestamp, optionally across all accounts.
- Offline full-text search across everything synced, composable with --json / --select / SQL.
- Slack AI ask/summarize/recap (from the Slack AI section above).

## Output and agent-native conventions

- `--json`, `--select <fields>`, `--compact`, `--csv`, `--quiet`, typed exit codes.
- `--dry-run` on send/mutating commands (default for sends).
- Local SQLite store with sync + FTS search, per account.
- Companion MCP server mirroring the command tree.

## Non-goals

- No OAuth / app-token path (that is the other, already-published slack CLI).
- No admin-only / enterprise-grid management surface unless it falls out for free.
- Not trying to evade Slack controls; this is your own session against your own workspaces.

## Remaining coordination (not blockers)

- For the Slack AI browser-sniff: I will need to point a capture at the workspace that has the paid add-on (which subdomain), with you logged into it in Chrome, and trigger a couple of AI actions while I record the calls.
