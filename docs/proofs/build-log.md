# slack-cookie build log

## Status: scaffold generated, hand-build remaining

Manifest transcendence rows: 9 planned, 0 built. Phase 3 will not pass until all 9 ship.

## Done

- Research brief + competitor absorb (slackdump, slack-slurp, slackattack, slack-mcp-server).
- Live browser-sniff (example.slack.com) mapped all 3 AI surfaces:
  - `search.inline` (AI answer), `ai.alpha.digest.*` (recap), Slackbot AI = chat.postMessage + poll conversations.replies, `ai.alpha.slackbot.skills.listEnabled`.
- Authored internal YAML spec (slack-cookie.yaml). Renamed reserved collisions: search→messages, dropped auth resource, client→unreads.
- Generated scaffold at working/slack-cookie-pp-cli. Builds clean (go build/vet/govulncheck PASS). Binary runs.
- Generated commands: chat (send), messages search (live), conversations list/history/replies/info/open, users list/info, unreads counts, plus framework search (offline FTS), sync, tail, doctor, auth, analytics, MCP server.

## Remaining (Phase 3 hand-build)

### Critical path: composed cookie auth (CLI does not authenticate without this)
- Generated auth is bearer_token (xoxc via Authorization) only. Must add:
  - `d` cookie sent as `Cookie: d=xoxd-...` header on every request (+ optional `d-s`).
  - Per-account workspace host override (example.slack.com vs others); base_url is currently https://slack.com/api.
  - d->xoxc exchange at login (slackattack pattern) so user can supply just the d cookie.
  - doctor that validates via /auth.test per account, invalidation-aware.
- Durable extension files (survive regen): internal/config/slackcookie_config.go, internal/client/slackcookie_headers.go, internal/cli/slackcookie_auth.go.

### Multi-account (separate logins)
- `[accounts.<name>]` config: workspace, xoxc, d_cookie (+ d-s). `default_account`.
- `--account` global flag, `SLACK_ACCOUNT` env, `--all-accounts`.
- Per-account SQLite DB file. `auth set-token --account`, `auth list`, `auth status`.

### 9 novel features
1. `ai ask "<q>"` — Slackbot AI assistant (post + poll conversations.replies). HEADLINE, robust.
2. `digest --all-accounts [--since]` — cross-workspace catch-up from per-account SQLite.
3. `missed --since <when> [--all-accounts]` — time-windowed mentions + DMs.
4. `ai search "<q>"` — search.inline AI answer (undocumented).
5. `ai recap [--since]` — Slackbot path or ai.alpha.digest.* (undocumented).
6. `stale-threads --days <n>`.
7. `trends --weeks <n>`.
8. `sql "<query>"` — raw SQL/FTS over synced data.
9. `ai skills` — ai.alpha.slackbot.skills.listEnabled.

### Then: shipcheck, dogfood (needs a real xoxc+d to live-test), polish.

## Key facts for the build
- Request: POST/GET https://<ws>.slack.com/api/<method>; xoxc via Authorization: Bearer (server accepts) OR token form field; d via Cookie header; believable User-Agent.
- Fragility: serialize, jitter, respect Retry-After, sync-first, avoid bulk users.list (invalidation in ~17s observed).
- Run dir: ~/printing-press/.runstate/vault-e0265611/runs/20260629-171901-slackcookie
- Working CLI: <run>/working/slack-cookie-pp-cli

## Update: composed cookie auth built + wired

- internal/config/slackcookie_config.go (durable): Account struct, ApplyAccount resolves active account from [accounts.*] config tables + flat env (SLACK_XOXC_TOKEN/SLACK_D_COOKIE/SLACK_D_S_COOKIE/SLACK_WORKSPACE/SLACK_ACCOUNT), sets cfg.SlackXoxcToken, cfg.Headers["Cookie"]=d=...; d-s=..., cfg.BaseURL=https://<ws>.slack.com/api.
- root.go: added --account persistent flag + rootFlags.account; newClient() calls config.ApplyAccount(cfg, f.account) between Load and New.
- No client.go edit needed: generated client already sends cfg.Headers on every request and uses cfg.BaseURL as host.
- Verified by dry-run: host -> example.slack.com, Authorization: Bearer xoxc. Cookie sent by client (not shown in dry-run).
- NEXT: live verify with real example xoxc + d, then build config-file auth commands (set-token/login/d->xoxc exchange), then 9 novel features.

## MILESTONE: composed cookie auth VERIFIED LIVE (example)

- auth.test -> {"ok":true,"team":"example","user":"example-user","team_id":"T_EXAMPLE_TEAM"}.
- CLI conversations list, messages search (live search.messages), conversations history all return real data with xoxc + d cookie. chat command has --channel/--text/--thread-ts.
- Root cause of earlier empty {}: stale response cache from a first run that used the literal placeholder cookie. --no-cache works. (Cookie is not in the cache key, but per-account xoxc+base_url differ so normal multi-account use is unaffected; consider adding cookie to cache key for the same-token-diff-cookie edge.)

## Known issues to fix (non-blocking for live use)
- Recurring warning "N/N items returned but not cached locally (no extractable ID field)": the generated list command's offline-cache row extraction doesn't find IDs (matters for --data-source local / sync, not live). Fix during sync/offline work.

## Next
- Auth UX so env-var export isn't needed every time: auth set-token --account / auth login (d->xoxc exchange) / auth list / auth status / real doctor (parse ok:true), writing [accounts.*] to config.toml.
- Then 9 novel features (headline: ai ask via Slackbot post+poll; digest --all-accounts).
