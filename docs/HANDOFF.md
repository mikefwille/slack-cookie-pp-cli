# slack-cookie CLI — session handoff

Single entry point for resuming this build in a new session. Read this first, then the linked docs.

## What this is

A Printing Press build of `slack-cookie`: a Slack CLI that authenticates as the user via their browser session (`xoxc` token + `d` cookie), NOT an OAuth app. Separate-login multi-account, offline SQLite per account, agent-native, with native Slack AI + Slackbot AI. Deliberate cookie-auth variant of the already-published token-based `slack` CLI (distinct name to avoid collision).

## Status: composed cookie auth BUILT + VERIFIED LIVE. Core absorbed surface works. 9 novel features + auth UX + verification remain.

## Paths

- Run dir: `~/printing-press/.runstate/vault-e0265611/runs/20260629-171901-slackcookie`
- Working CLI (build/edit here): `<run>/working/slack-cookie-pp-cli`
- Binary: `<run>/working/slack-cookie-pp-cli/slack-cookie-pp-cli` (rebuild: `cd <cli> && go build -o ./slack-cookie-pp-cli ./cmd/slack-cookie-pp-cli`)
- Generator binary: `cli-printing-press` (on PATH, v4.27.0). Go 1.26.4.

## Key docs (in run dir)

- `research/build-spec.md` — requirements + all locked decisions (multi-account flavor, Slack AI interpretation, etc.).
- `research/absorb-manifest.md` — the APPROVED 14 absorbed + 9 novel features. This is the build contract.
- `discovery/slack-ai-sniff.md` — live-sniffed Slack AI endpoints (the undocumented part).
- `proofs/build-log.md` — running build log with the milestone + known issues.
- `slack-cookie.yaml` — the internal spec the generator ran on.
- `research.json` — README/SKILL narrative + competitor credits.

## What's DONE

- Research, live AI browser-sniff, approved absorb manifest.
- Generated scaffold: `chat` (send), `messages search` (live search.messages), `conversations list/history/replies/info/open`, `users list/info`, `unreads`, framework `search`/`sync`/`tail`/`doctor`/`auth`, SQLite store, MCP server.
- Composed cookie auth (the hard part), hand-authored + regen-safe:
  - `internal/config/slackcookie_config.go` — `Account` struct + `ApplyAccount(cfg, account)`: resolves active account from `[accounts.*]` config tables and flat env (`SLACK_XOXC_TOKEN`, `SLACK_D_COOKIE`, `SLACK_D_S_COOKIE`, `SLACK_WORKSPACE`, `SLACK_ACCOUNT`); sets `cfg.SlackXoxcToken` (-> `Authorization: Bearer`), `cfg.Headers["Cookie"]="d=...; d-s=..."`, `cfg.BaseURL="https://<ws>.slack.com/api"`.
  - `internal/cli/root.go` (3 small edits): `--account` persistent flag, `rootFlags.account`, and `config.ApplyAccount(cfg, f.account)` inserted in `newClient()` between `config.Load` and `client.New`.
  - No `client.go` edit needed: the generated client already sends `cfg.Headers` on every request and uses `cfg.BaseURL` as host. This is the key insight.
- VERIFIED LIVE against example.slack.com: `auth.test` -> ok:true (team example, user example-user), and CLI `conversations list` / `messages search` / `conversations history` all return real data. `chat` has `--channel/--text/--thread-ts`.

## What's LEFT (in order)

1. Auth UX (so users stop exporting env vars): `auth login`/`set-token --account <a> --workspace <w>` writing `[accounts.<a>]` to config.toml; `auth list`; `auth status [--account]`; a real `doctor` that calls `/auth.test` and parses `ok:true`. Optional: `d`->`xoxc` exchange so user supplies only the `d` cookie (slackattack pattern). Edit the generated `internal/cli/auth.go` + `doctor.go`, or add `internal/cli/slackcookie_auth.go`.
2. The 9 novel features (hand-built Go in `internal/cli/`, wired in root.go; see absorb-manifest transcendence table). Order:
   - `ai ask "<q>"` (HEADLINE, robust): post the question to the Slackbot conversation via `chat.postMessage`, then poll `conversations.replies` until the assistant reply finishes; print it. (Slackbot AI = ai.alpha.slackbot.*; the user specifically wants this.)
   - `digest --all-accounts [--since]`: cross-workspace catch-up from per-account SQLite.
   - `missed --since <when> [--all-accounts]`; `ai search "<q>"` (search.inline); `ai recap [--since]`; `stale-threads --days`; `trends --weeks`; `sql "<q>"`; `ai skills` (ai.alpha.slackbot.skills.listEnabled).
3. Multi-account polish: per-account SQLite DB files; make `sync`/offline commands account-aware.
4. Fix known issue: offline-cache "no extractable ID field" warning (list commands don't cache rows to SQLite — matters for `--data-source local`/sync, not live).
5. Then: `cli-printing-press shipcheck --dir <cli> --spec <run>/slack-cookie.yaml --research-dir <run>`, live dogfood (needs fresh xoxc+d from user), `/printing-press-polish <cli>`.

## How to live-test (creds are NOT stored anywhere; user re-provides)

User grabs from a logged-in Slack browser tab: `xoxc` from DevTools console
`Object.values(JSON.parse(localStorage.localConfig_v2).teams).find(t=>t.url.includes('<ws>')).token`,
and `d` cookie from DevTools > Application > Cookies > app.slack.com > `d`. Then:
```
export SLACK_WORKSPACE=example SLACK_XOXC_TOKEN=xoxc-... SLACK_D_COOKIE=xoxd-...
<cli>/slack-cookie-pp-cli conversations list --agent --no-cache --select id,name,is_private
```
Note: response cache key currently omits the cookie; use `--no-cache` when changing the cookie under a fixed token. Slack invalidates xoxc/xoxd on bursty access (e.g. bulk users.list ~17s) — keep it slow / sync-first.

## Resume prompt to paste into a new session

"Resume the slack-cookie CLI build. Read the handoff at
`~/printing-press/.runstate/vault-e0265611/runs/20260629-171901-slackcookie/HANDOFF.md`
and continue from 'What's LEFT'. Start with the auth UX, then `ai ask`."
