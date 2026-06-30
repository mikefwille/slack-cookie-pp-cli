# Slack (cookie auth) CLI

**Drive Slack from the terminal as yourself using your browser session, across all your separate-login workspaces, with offline sync and native Slack AI.**

Authenticates with your own xoxc session token plus the d cookie, so there's no app to create and no admin approval. Every workspace you're logged into becomes a named account with its own local SQLite mirror, so you can search, catch up, and run cross-workspace digests offline. Includes ai ask, which routes through Slack's own AI assistant.

Created by [@mikefwille](https://github.com/mikefwille) (Mike W).

## Install

The recommended path installs both the `slack-cookie-pp-cli` binary and the `pp-slack-cookie` agent skill (Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, and other agents supported by the upstream [`skills`](https://github.com/vercel-labs/skills) CLI) in one shot:

```bash
npx -y @mvanhorn/printing-press-library install slack-cookie
```

For CLI only (no skill):

```bash
npx -y @mvanhorn/printing-press-library install slack-cookie --cli-only
```

For skill only — installs the skill into the same agents as the default command above, but skips the CLI binary (use this to update or reinstall just the skill):

```bash
npx -y @mvanhorn/printing-press-library install slack-cookie --skill-only
```

To constrain the skill install to one or more specific agents (repeatable — agent names match the [`skills`](https://github.com/vercel-labs/skills) CLI):

```bash
npx -y @mvanhorn/printing-press-library install slack-cookie --agent claude-code
npx -y @mvanhorn/printing-press-library install slack-cookie --agent claude-code --agent codex
```

### Without Node (Go fallback)

If `npx` isn't available (no Node, offline), install the CLI directly via Go (requires Go 1.26.4 or newer):

```bash
go install github.com/mvanhorn/printing-press-library/library/productivity/slack-cookie/cmd/slack-cookie-pp-cli@latest
```

This installs the CLI only — no skill.

### Pre-built binary

Download a pre-built binary for your platform from the [latest release](https://github.com/mvanhorn/printing-press-library/releases/tag/slack-cookie-current). On macOS, clear the Gatekeeper quarantine: `xattr -d com.apple.quarantine <binary>`. On Unix, mark it executable: `chmod +x <binary>`.

<!-- pp-hermes-install-anchor -->
## Install for Hermes

Install the CLI binary first. The installer writes binaries to a per-user managed bin directory by default: `$HOME/.local/bin` on macOS/Linux and `%LOCALAPPDATA%\Programs\PrintingPress\bin` on Windows.

```bash
npx -y @mvanhorn/printing-press-library install slack-cookie --cli-only
```

Then install the focused Hermes skill.

From the Hermes CLI:

```bash
hermes skills install mvanhorn/printing-press-library/cli-skills/pp-slack-cookie --force
```

Inside a Hermes chat session:

```bash
/skills install mvanhorn/printing-press-library/cli-skills/pp-slack-cookie --force
```

Restart the Hermes session or gateway if the newly installed skill is not visible immediately.

## Install for OpenClaw
Install both the CLI binary and the focused OpenClaw skill. The installer defaults binaries to a per-user bin directory (`$HOME/.local/bin` on macOS/Linux, `%LOCALAPPDATA%\Programs\PrintingPress\bin` on Windows):

```bash
npx -y @mvanhorn/printing-press-library install slack-cookie --agent openclaw
```

Restart the OpenClaw session or gateway if the newly installed skill is not visible immediately.

## Use with Claude Desktop

This CLI ships an [MCPB](https://github.com/modelcontextprotocol/mcpb) bundle — Claude Desktop's standard format for one-click MCP extension installs (no JSON config required).

To install:

1. Download the `.mcpb` for your platform from the [latest release](https://github.com/mvanhorn/printing-press-library/releases/tag/slack-cookie-current).
2. Double-click the `.mcpb` file. Claude Desktop opens and walks you through the install.
3. Fill in `SLACK_XOXC_TOKEN` when Claude Desktop prompts you.

Requires Claude Desktop 1.0.0 or later. Pre-built bundles ship for macOS Apple Silicon (`darwin-arm64`) and Windows (`amd64`, `arm64`); for other platforms, use the manual config below.

<details>
<summary>Manual JSON config (advanced)</summary>

If you can't use the MCPB bundle (older Claude Desktop, unsupported platform), install the MCP binary and configure it manually.


```bash
go install github.com/mvanhorn/printing-press-library/library/productivity/slack-cookie/cmd/slack-cookie-pp-mcp@latest
```

Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "slack-cookie": {
      "command": "slack-cookie-pp-mcp",
      "env": {
        "SLACK_XOXC_TOKEN": "<your-key>"
      }
    }
  }
}
```

</details>

## Authentication

Uses your logged-in browser session: the xoxc token (Authorization) plus the d cookie. Provide them per account via env vars (SLACK_XOXC_TOKEN, SLACK_D_COOKIE) or `auth login`. The d cookie can be exchanged for a fresh xoxc automatically. Session tokens rotate and can be invalidated by Slack on bursty access, so the CLI syncs to a local mirror and paginates conservatively; run `doctor` to check validity per account.

## Quick Start

```bash
# health check, works without auth
slack-cookie-pp-cli doctor --dry-run


# list channels and DMs in the active account
slack-cookie-pp-cli conversations list --json


# search messages
slack-cookie-pp-cli search "q3 planning" --json

```

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

## Usage

Run `slack-cookie-pp-cli --help` for the full command reference and flag list.

## Paths & environment variables

This CLI separates local files into four path kinds:

| Kind | Contents |
|------|----------|
| `config` | User-editable settings such as `config.toml` and saved profiles |
| `data` | Durable local data: `credentials.toml`, `data.db`, cookies, browser-session proof files, and other auth sidecars |
| `state` | Runtime state such as persisted queries, jobs, and `teach.log` |
| `cache` | Regenerable HTTP/cache files |

Each kind resolves independently. The ladder is:

1. Per-kind env var: `SLACK_COOKIE_CONFIG_DIR`, `SLACK_COOKIE_DATA_DIR`, `SLACK_COOKIE_STATE_DIR`, or `SLACK_COOKIE_CACHE_DIR`
2. `--home <dir>` for this invocation
3. `SLACK_COOKIE_HOME` for a flat relocated root
4. XDG env vars: `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`
5. Platform defaults matching existing installs

For containers and agent sandboxes, prefer a single relocated root:

```bash
export SLACK_COOKIE_HOME=/srv/slack-cookie
slack-cookie-pp-cli doctor
```

Under `SLACK_COOKIE_HOME=/srv/slack-cookie`, the four dirs resolve to `/srv/slack-cookie/config`, `/srv/slack-cookie/data`, `/srv/slack-cookie/state`, and `/srv/slack-cookie/cache`.

MCP servers do not receive CLI flags from the host. Put relocation in the host `env` block:

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

Precedence matters in fleets: an ambient per-kind variable such as `SLACK_COOKIE_DATA_DIR` overrides an explicit `--home` for that kind. Use `SLACK_COOKIE_HOME` or the per-kind variables for durable fleet relocation; treat `--home` as the weaker per-invocation lever.

Relocation is one-way. Unsetting `SLACK_COOKIE_HOME` does not move files back to platform defaults, and `doctor` cannot find credentials left under a former root. Move the files manually before unsetting relocation variables.

Existing installs keep working because the platform-default rung matches the legacy layout. On the first auth write, stored secrets leave `config.toml` and are consolidated into `credentials.toml` under the data directory. Run `slack-cookie-pp-cli doctor --fail-on warn` to check path and credential-location warnings in automation.

## Commands

### chat

Send messages

- **`slack-cookie-pp-cli chat`** - Send a message to a channel, DM, or thread

### conversations

Channels, DMs, and group DMs

- **`slack-cookie-pp-cli conversations history`** - Read messages from a conversation
- **`slack-cookie-pp-cli conversations info`** - Get a conversation's metadata
- **`slack-cookie-pp-cli conversations list`** - List channels, DMs, and group DMs
- **`slack-cookie-pp-cli conversations open`** - Open or resume a DM/group DM
- **`slack-cookie-pp-cli conversations replies`** - Read replies in a thread

### messages

Live message search

- **`slack-cookie-pp-cli messages <query>`** - Full-text search messages (live API)

### unreads

Unread counts and state

- **`slack-cookie-pp-cli unreads`** - Unread counts and state for all conversations

### users

Workspace users

- **`slack-cookie-pp-cli users info`** - Get a user's profile
- **`slack-cookie-pp-cli users list`** - List workspace users (paginate slowly to avoid token invalidation)


## Output Formats

```bash
# Human-readable table (default in terminal, JSON when piped)
slack-cookie-pp-cli conversations list

# JSON for scripting and agents
slack-cookie-pp-cli conversations list --json

# Filter to specific fields
slack-cookie-pp-cli conversations list --json --select id,name,status

# Dry run — show the request without sending
slack-cookie-pp-cli conversations list --dry-run

# Agent mode — JSON + compact + no prompts in one flag
slack-cookie-pp-cli conversations list --agent
```

## Agent Usage

This CLI is designed for AI agent consumption:

- **Non-interactive** - never prompts, every input is a flag
- **Pipeable** - `--json` output to stdout, errors to stderr
- **Filterable** - `--select id,name` returns only fields you need
- **Previewable** - `--dry-run` shows the request without sending
- **Explicit retries** - add `--idempotent` to create retries when a no-op success is acceptable
- **Confirmable** - `--yes` for explicit confirmation of destructive actions
- **Piped input** - write commands can accept structured input when their help lists `--stdin`
- **Offline-friendly** - sync/search commands can use the local SQLite store when available
- **Agent-safe by default** - no colors or formatting unless `--human-friendly` is set

Exit codes: `0` success, `2` usage error, `3` not found, `4` auth error, `5` API error, `7` rate limited, `10` config error.

## Health Check

```bash
slack-cookie-pp-cli doctor
```

Verifies configuration, credentials, and connectivity to the API.

## Configuration

Run `slack-cookie-pp-cli doctor` to see the resolved config, data, state, and cache directories. The platform-default config path is `~/.config/slack-cookie/config.toml`; `--home`, `SLACK_COOKIE_HOME`, and per-kind env vars can relocate it.

Static request headers can be configured under `headers`; per-command header overrides take precedence.

Environment variables:

| Name | Kind | Required | Description |
| --- | --- | --- | --- |
| `SLACK_XOXC_TOKEN` | per_call | Yes | Set to your API credential. |

### agentcookie (optional)

If you use agentcookie to sync secrets across machines, this CLI auto-adopts agentcookie-managed credentials with no extra setup. When the daemon writes to this CLI's config, `slack-cookie-pp-cli doctor` reports `agentcookie: detected` and `auth-status` labels the source as `agentcookie`. Skip this section if you don't use agentcookie - the CLI works the same as any other.

## Troubleshooting
**Authentication errors (exit code 4)**
- Run `slack-cookie-pp-cli doctor` to check credentials
- Verify the environment variable is set: `echo $SLACK_XOXC_TOKEN`
**Not found errors (exit code 3)**
- Check the resource ID is correct
- Run the `list` command to see available items

### API-specific

- **invalid_auth** — Your xoxc/d session was invalidated; re-grab the d cookie and run `auth login`, or run `doctor`.
- **Rate limited (429)** — Slow down: rely on `sync` + offline queries instead of repeated live calls; respect Retry-After.

---

## Sources & Inspiration

This CLI was built by studying these projects and resources:

- [**slackdump**](https://github.com/rusq/slackdump) — Go (2700 stars)
- [**slack-slurp**](https://github.com/NoF0rte/slack-slurp) — Go
- [**slackattack**](https://github.com/fr4nk3nst1ner/slackattack) — Python
- [**slack-mcp-server**](https://github.com/korotovsky/slack-mcp-server) — Go

Generated by [CLI Printing Press](https://github.com/mvanhorn/cli-printing-press)
