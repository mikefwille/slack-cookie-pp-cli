---
name: pp-slack-cookie
description: "Drive Slack from the terminal as yourself using your browser session, across all your separate-login workspaces, with offline sync and native Slack AI. Trigger phrases: `send a slack message`, `search slack`, `what did I miss on slack`, `catch me up on slack`, `use slack-cookie`, `run slack-cookie`."
author: "Mike W"
license: "Apache-2.0"
argument-hint: "<command> [args] | install cli|mcp"
allowed-tools: "Read Bash"
metadata:
  openclaw:
    requires:
      bins:
        - slack-cookie-pp-cli
    install:
      - kind: go
        bins: [slack-cookie-pp-cli]
        module: github.com/mvanhorn/printing-press-library/library/productivity/slack-cookie/cmd/slack-cookie-pp-cli
---

# Slack (cookie auth) — Printing Press CLI

## Prerequisites: Install the CLI

This skill drives the `slack-cookie-pp-cli` binary. **You must verify the CLI is installed before invoking any command from this skill.** If it is missing, install it first:

1. Install via the Printing Press installer. It defaults binaries to `$HOME/.local/bin` on macOS/Linux and `%LOCALAPPDATA%\Programs\PrintingPress\bin` on Windows:
   ```bash
   npx -y @mvanhorn/printing-press-library install slack-cookie --cli-only
   ```
2. Verify: `slack-cookie-pp-cli --version`
3. Ensure the reported install directory is on `$PATH` for the agent/runtime that will invoke this skill.

If the `npx` install fails (no Node, offline, etc.), fall back to a direct Go install (requires Go 1.26.4 or newer). This installs into `$GOPATH/bin` (default `$HOME/go/bin`), so add that directory to `$PATH` instead:

```bash
go install github.com/mvanhorn/printing-press-library/library/productivity/slack-cookie/cmd/slack-cookie-pp-cli@latest
```

If `--version` reports "command not found" after install, the runtime cannot see the binary directory on `$PATH`. Do not proceed with skill commands until verification succeeds.

Authenticates with your own xoxc session token plus the d cookie, so there's no app to create and no admin approval. Every workspace you're logged into becomes a named account with its own local SQLite mirror, so you can search, catch up, and run cross-workspace digests offline. Includes ai ask, which routes through Slack's own AI assistant.

## When to Use This CLI

Use when you want to read, search, send, and catch up on Slack as yourself from the terminal or an agent, especially across multiple separate-login workspaces, without creating a Slack app. Strong for offline/cheap repeated queries via the local mirror and for cross-workspace digests.

## Anti-triggers

Do not use this CLI for:
- Building a shared bot or integration for a team (use an official Slack app + bot token instead)
- Admin / enterprise-grid workspace management
- Anything requiring OAuth scopes you'd grant to a third party

## Command Reference

**chat** — Send messages

- `slack-cookie-pp-cli chat` — Send a message to a channel, DM, or thread

**conversations** — Channels, DMs, and group DMs

- `slack-cookie-pp-cli conversations history` — Read messages from a conversation
- `slack-cookie-pp-cli conversations info` — Get a conversation's metadata
- `slack-cookie-pp-cli conversations list` — List channels, DMs, and group DMs
- `slack-cookie-pp-cli conversations open` — Open or resume a DM/group DM
- `slack-cookie-pp-cli conversations replies` — Read replies in a thread

**messages** — Live message search

- `slack-cookie-pp-cli messages <query>` — Full-text search messages (live API)

**unreads** — Unread counts and state

- `slack-cookie-pp-cli unreads` — Unread counts and state for all conversations

**users** — Workspace users

- `slack-cookie-pp-cli users info` — Get a user's profile
- `slack-cookie-pp-cli users list` — List workspace users (paginate slowly to avoid token invalidation)


### Finding the right command

When you know what you want to do but not which command does it, ask the CLI directly:

```bash
slack-cookie-pp-cli which "<capability in your own words>"
```

`which` resolves a natural-language capability query to the best matching command from this CLI's curated feature index. Exit code `0` means at least one match; exit code `2` means no confident match — fall back to `--help` or use a narrower query.

## Recipes


### Catch up across every workspace

```bash
slack-cookie-pp-cli digest --all-accounts --since 1d
```

One cross-workspace rollup of what you missed.

### Ask Slack's AI

```bash
slack-cookie-pp-cli ai ask "what did I miss in #product today?"
```

Routes through Slack's AI assistant and prints the answer.

### Narrow a verbose response

```bash
slack-cookie-pp-cli conversations list --agent --select id,name,is_private
```

Bounded, agent-friendly output from a large list.

## Auth Setup

Uses your logged-in browser session: the xoxc token (Authorization) plus the d cookie. Provide them per account via env vars (SLACK_XOXC_TOKEN, SLACK_D_COOKIE) or `auth login`. The d cookie can be exchanged for a fresh xoxc automatically. Session tokens rotate and can be invalidated by Slack on bursty access, so the CLI syncs to a local mirror and paginates conservatively; run `doctor` to check validity per account.

Run `slack-cookie-pp-cli doctor` to verify setup.

## Agent Mode

Add `--agent` to any command. Expands to: `--json --compact --no-input --no-color --yes`.

- **Pipeable** — JSON on stdout, errors on stderr
- **Filterable** — `--select` keeps a subset of fields. Dotted paths descend into nested structures; arrays traverse element-wise. Critical for keeping context small on verbose APIs:

  ```bash
  slack-cookie-pp-cli conversations list --agent --select id,name,status
  ```
- **Previewable** — `--dry-run` shows the request without sending
- **Offline-friendly** — sync/search commands can use the local SQLite store when available
- **Non-interactive** — never prompts, every input is a flag
- **Explicit retries** — use `--idempotent` only when an already-existing create should count as success

### Response envelope

Commands that read from the local store or the API wrap output in a provenance envelope:

```json
{
  "meta": {"source": "live" | "local", "synced_at": "...", "reason": "..."},
  "results": <data>
}
```

Parse `.results` for data and `.meta.source` to know whether it's live or local. A human-readable `N results (live)` summary is printed to stderr only when stdout is a terminal AND no machine-format flag (`--json`, `--csv`, `--compact`, `--quiet`, `--plain`, `--select`) is set — piped/agent consumers and explicit-format runs get pure JSON on stdout.

## Paths and state

Agents should treat the CLI's path resolver as part of the runtime contract:

- Use `--home <dir>` for one invocation, or set `SLACK_COOKIE_HOME=<dir>` to relocate all four path kinds under one root.
- Use per-kind env vars only when a specific kind must diverge: `SLACK_COOKIE_CONFIG_DIR`, `SLACK_COOKIE_DATA_DIR`, `SLACK_COOKIE_STATE_DIR`, `SLACK_COOKIE_CACHE_DIR`.
- Resolution order is per-kind env var, `--home`, `SLACK_COOKIE_HOME`, XDG (`XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`), then platform defaults.
- `config` contains settings like `config.toml` and profiles. `data` contains `credentials.toml`, `data.db`, cookies, and auth sidecars. `state` contains persisted queries, jobs, and `teach.log`. `cache` contains regenerable HTTP/cache files.
- Stored secrets live in `credentials.toml` under the data dir. Existing legacy `config.toml` secrets are read for compatibility and leave `config.toml` on the first auth write.
- Run `slack-cookie-pp-cli doctor --fail-on warn` to surface path and credential-location warnings. `agent-context` exposes a schema v4 `paths` block for agents that need the resolved dirs.
- For MCP, pass relocation through the MCP host config. The MCP binary does not inherit CLI flags:

  ```json
  {
    "mcpServers": {
      "slack-cookie": {
        "command": "slack-cookie-pp-mcp",
        "env": {
          "SLACK_COOKIE_HOME": "/srv/slack-cookie"
        }
      }
    }
  }
  ```

Fleet precedence: an inherited per-kind env var overrides an explicit `--home` for that kind. Use `SLACK_COOKIE_HOME` or per-kind vars as durable fleet levers, and use `--home` only for a single invocation. Relocation is not reversible by unsetting env vars; move files manually before clearing `SLACK_COOKIE_HOME`, or `doctor` will not find credentials left under the former root.

## Agent Feedback

When you (or the agent) notice something off about this CLI, record it:

```
slack-cookie-pp-cli feedback "the --since flag is inclusive but docs say exclusive"
slack-cookie-pp-cli feedback --stdin < notes.txt
slack-cookie-pp-cli feedback list --json --limit 10
```

Entries are stored locally as `feedback.jsonl` under the resolved data dir. They are never POSTed unless `SLACK_COOKIE_FEEDBACK_ENDPOINT` is set AND either `--send` is passed or `SLACK_COOKIE_FEEDBACK_AUTO_SEND=true`. Default behavior is local-only.

Write what *surprised* you, not a bug report. Short, specific, one line: that is the part that compounds.

## Output Delivery

Every command accepts `--deliver <sink>`. The output goes to the named sink in addition to (or instead of) stdout, so agents can route command results without hand-piping. Three sinks are supported:

| Sink | Effect |
|------|--------|
| `stdout` | Default; write to stdout only |
| `file:<path>` | Atomically write output to `<path>` (tmp + rename) |
| `webhook:<url>` | POST the output body to the URL (`application/json` or `application/x-ndjson` when `--compact`) |

Unknown schemes are refused with a structured error naming the supported set. Webhook failures return non-zero and log the URL + HTTP status on stderr.

## Named Profiles

A profile is a saved set of flag values, reused across invocations. Use it when a scheduled agent calls the same command every run with the same configuration - HeyGen's "Beacon" pattern.

```
slack-cookie-pp-cli profile save briefing --json
slack-cookie-pp-cli --profile briefing conversations list
slack-cookie-pp-cli profile list --json
slack-cookie-pp-cli profile show briefing
slack-cookie-pp-cli profile delete briefing --yes
```

Explicit flags always win over profile values; profile values win over defaults. `agent-context` lists all available profiles under `available_profiles` so introspecting agents discover them at runtime.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 2 | Usage error (wrong arguments) |
| 3 | Resource not found |
| 4 | Authentication required |
| 5 | API error (upstream issue) |
| 7 | Rate limited (wait and retry) |
| 10 | Config error |

## Argument Parsing

Parse `$ARGUMENTS`:

1. **Empty, `help`, or `--help`** → show `slack-cookie-pp-cli --help` output
2. **Starts with `install`** → ends with `mcp` → MCP installation; otherwise → see Prerequisites above
3. **Anything else** → Direct Use (execute as CLI command with `--agent`)

## MCP Server Installation

1. Install the MCP server:
   ```bash
   go install github.com/mvanhorn/printing-press-library/library/productivity/slack-cookie/cmd/slack-cookie-pp-mcp@latest
   ```
2. Register with Claude Code:
   ```bash
   claude mcp add slack-cookie-pp-mcp -- slack-cookie-pp-mcp
   ```
3. Verify: `claude mcp list`

## Direct Use

1. Check if installed: `which slack-cookie-pp-cli`
   If not found, offer to install (see Prerequisites at the top of this skill).
2. Match the user query to the best command from the Unique Capabilities and Command Reference above.
3. Execute with the `--agent` flag:
   ```bash
   slack-cookie-pp-cli <command> [subcommand] [args] --agent
   ```
4. If ambiguous, drill into subcommand help: `slack-cookie-pp-cli <command> --help`.
