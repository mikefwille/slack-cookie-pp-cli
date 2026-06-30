# slack-cookie Absorb Manifest

Cookie-auth (`xoxc` + `d`) Slack CLI, separate-login multi-account, agent-native, offline SQLite, native Slack AI + Slackbot AI. Auth/host/format and all AI endpoints confirmed by live sniff against example.slack.com.

## Absorbed (match or beat every existing tool)

| # | Feature | Best source | Our implementation | Added value |
|---|---------|-------------|--------------------|-------------|
| 1 | Send message (channel/DM/thread) | slackdump, slack-mcp-server | `slack-cookie-pp-cli send` | dry-run default, multi-account, `--thread`, agent-native |
| 2 | Search messages | slack-slurp, slackdump | `slack-cookie-pp-cli search` | live (`search.messages`) + offline FTS, `--json/--select`, cross-account |
| 3 | List channels/DMs/mpims | slackdump | `slack-cookie-pp-cli channels list` | offline, `--types`, filters |
| 4 | Read channel history | slackdump | `slack-cookie-pp-cli history` | offline cache, cursor paging, throttle-safe |
| 5 | Read thread replies | slackdump | `slack-cookie-pp-cli thread` | offline, resolves authors |
| 6 | List / resolve users | slackdump, slack-slurp | `slack-cookie-pp-cli users` | slow-paginated to avoid invalidation, cached |
| 7 | Archive / export a channel | slackdump | `slack-cookie-pp-cli export` | JSONL/JSON, resumable from sync |
| 8 | Download files | slackattack | `slack-cookie-pp-cli files get` | |
| 9 | Unread counts / catch-up | (client.counts) | `slack-cookie-pp-cli unreads` | uses `client.counts`, one call for all unreads |
| 10 | Sync workspace to local store | slackdump | `slack-cookie-pp-cli sync` | per-account SQLite + FTS, incremental |
| 11 | d-cookie -> xoxc exchange at login | slackattack | `slack-cookie-pp-cli auth login --account <a> --cookie <d>` | mints xoxc from the `d` cookie so you supply one value |
| 12 | Import creds from desktop app | slacktokens | `slack-cookie-pp-cli auth import-desktop` (macOS) | convenience; optional |
| 13 | MCP server | slackdump, slack-mcp-server | companion MCP mirroring the command tree | |
| 14 | Health / auth validity | slack-mcp-server | `slack-cookie-pp-cli doctor` | per-account, invalidation-aware |

## Transcendence (only possible with our approach)

| # | Feature | Command | Buildability | Why only we can do this |
|---|---------|---------|--------------|-------------------------|
| 1 | Ask Slack's AI anything | `ai ask "<q>"` | hand-code | Routes through the Slackbot AI assistant (post + poll). Answers, summarizes, recaps. Documented methods, no app needed. |
| 2 | Cross-workspace digest | `digest --all-accounts [--since]` | hand-code | Joins synced data across every account's SQLite DB. No token/web tool spans separate-login workspaces. |
| 3 | What did I miss | `missed --since <when> [--all-accounts]` | hand-code | Time-windowed personal mentions + DMs from local store, optionally across accounts. |
| 4 | Inline AI search answer | `ai search "<q>"` | hand-code | `search.inline` AI answer card surfaced to the terminal. |
| 5 | Native recap/digest | `ai recap [--since]` | hand-code | Slackbot "what did I miss" path (robust) or `ai.alpha.digest.*` (native). |
| 6 | Stale-thread finder | `stale-threads --days <n>` | hand-code | Threads mentioning me / I'm in with no reply in N days, from local store. |
| 7 | Activity trends | `trends --weeks <n>` | hand-code | Per-channel message volume by week from sync snapshots. |
| 8 | Offline SQL over Slack | `sql "<query>"` / `search --offline` | hand-code | FTS + raw SQL over everything synced, composable with `--json/--select`. |
| 9 | List AI skills | `ai skills` | hand-code | `ai.alpha.slackbot.skills.listEnabled` — what the assistant can do. |

## Notes for the build

- Auth confirmed: `POST https://<ws>.slack.com/api/<method>`, `xoxc` as `token` form field, `d` as `Cookie: d=...`, optional `d-s`. Set believable `User-Agent` + `_x_reason`. Enterprise Grid: `<ws>.enterprise.slack.com` + `slack_route`.
- Fragility: serialize calls, jitter, respect `Retry-After`, cache hard, avoid bulk `users.list`. Sync-first design is a correctness feature.
- AI endpoints confirmed by sniff: `search.inline`, `ai.alpha.digest.listDigestChannelIds`, `ai.alpha.slackbot.skills.listEnabled`, plus the Slackbot post+poll model.
