# slack-cookie CLI Brief

## API Identity
- Domain: Slack Web API, authenticated as a logged-in browser session (`xoxc-` token + `d` cookie), not an OAuth app.
- Host: `https://<workspace>.slack.com/api/<method>` (apex `slack.com/api` also works). Enterprise Grid: `<workspace>.enterprise.slack.com` + `slack_route` param.
- Users: power users / agents who want to drive their own Slack across multiple separate-login workspaces without app creation or admin approval.
- Data profile: channels, DMs/mpims, messages, threads, users, unread counts, search results, Slack AI answers/summaries/recaps.

## Auth
- `xoxc-` token sent as the `token` form field in a urlencoded/multipart POST body (browser behavior). `Authorization: Bearer xoxc-...` is also accepted server-side.
- `d` cookie sent as `Cookie: d=xoxd-...`. HttpOnly; extract from DevTools > Application > Cookies on app.slack.com. Optional `d-s` companion cookie.
- Extraction: `xoxc` from `localStorage.localConfig_v2` (`teams[teamId].token`) or any request payload's `token` field; `d` from the cookie store.
- Auth-UX option to evaluate: exchange a bare `d` cookie for an `xoxc` token via Slack's backend (per slackattack), so setup can take just the `d` cookie.
- Fingerprint/believability: set `Origin: https://app.slack.com`, a browser-like `User-Agent`, and a meaningful `_x_reason` per call. Web client also sends `_x_id`, `_x_csid`, `_x_version_ts`, etc. (not cryptographically validated for most methods).

## Reachability Risk
- High-fragility for session tokens. Slack invalidates `xoxc/xoxd` on suspicious patterns: bulk `users.list` returned `invalid_auth` within ~17s in a documented case. `xoxp` is treated more leniently.
- Mitigation (design-level): serialize requests, add jitter, respect `Retry-After` (429), cache to SQLite to minimize calls, avoid bulk fan-out, set `_x_reason`.
- `conversations.history`/`replies` May 2025 restriction (1 req/min, limit 15) targets non-Marketplace apps; session-token behavior may differ but treat conservatively.
- `d` cookie TTL shortened from 10y but still long-lived; expect re-extraction after weeks/months. `doctor` reports validity per account.

## Multi-account
- Separate logins: each account = `{ workspace subdomain, xoxc, d (+ optional d-s) }`. Per-account SQLite DB. `--account` / `--all-accounts`.

## Top Workflows
1. Send a message as me (channel/DM/thread), default `--dry-run`.
2. Search messages across a workspace (and across all accounts).
3. Read/catch up: list channels, read history/threads, unread digest.
4. Ask Slack AI a question and get the answer out; summarize a thread; recap a channel.
5. Sync a workspace to local SQLite and run fast offline/agent queries.

## Table Stakes (absorb from competitors)
- slackdump (Go, 2.7k★): archive/export channels+threads+files+users, incremental resume, search, MCP, built-in edge/undocumented API wrapper.
- slack-slurp (Go): message search, recon, user/channel enumeration, secret detection.
- slackattack (Py): channel/user enumeration, file download, d->xoxc exchange.
- slack-mcp-server (Go): MCP read+send; documents the users.list invalidation risk.
- slacktokens / patched-slack-sdk: credential extraction from the desktop app local store (optional convenience, macOS).

## Methods (documented + internal)
- Send: `chat.postMessage` (channel, text, thread_ts, blocks).
- Search: `search.messages` (query, count<=100, page, sort), `search.all`; internal `search.modules.messages`.
- Conversations: `conversations.list` (types, cursor), `conversations.history` (channel, oldest/latest, cursor), `conversations.replies` (channel, ts), `conversations.open` (users).
- Users: `users.list` (cursor; throttle hard), `users.info` (user).
- Internal boot/unread: `client.userBoot`, `client.counts` (unread state without per-channel history), `users.counts`.

## Slack AI (must browser-sniff)
- Confirmed public: `slackAi.permissions.getForUser` (gating check only).
- Functional endpoints (ask/search, thread summary, channel recap) are undocumented. Likely `slackAi.*` namespace. Discover via sniff against example.slack.com (has the paid add-on): capture the `/api/` POSTs (filter `slackAi`, `summar`, `recap`, `search`) and record method names + request/response shapes.

## Data Layer
- Primary entities: accounts, channels/conversations, users, messages (with thread_ts), search results cache, unread counts, ai_answers (cached Q/A + citations).
- Sync cursor: per-conversation `latest` ts; per-account.
- FTS: messages full-text for offline search composable with --json/--select/SQL.

## Product Thesis
- Name: slack-cookie. Every useful Slack action driven by your own session, across all your separate-login workspaces, with offline SQLite sync, agent-native output, native Slack AI access, and cross-workspace digests no token-based or web tool offers.

## Build Priorities
1. Auth layer: per-account config, xoxc/d (+d-s) request signing, optional d->xoxc exchange, doctor with invalidation-aware checks.
2. Data layer: per-account SQLite + sync (channels, users, messages, FTS, unread).
3. Absorb: send (dry-run default), search, conversations list/history/replies, users, archive/export.
4. Transcend: cross-workspace digest, "what did I miss", stale threads, activity trends, Slack AI ask/summarize/recap.
