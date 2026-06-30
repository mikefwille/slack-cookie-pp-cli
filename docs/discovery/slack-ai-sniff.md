# Slack AI browser-sniff (example.slack.com)

Captured live from the logged-in web client via the Chrome extension network reader. All calls are `POST https://example.slack.com/api/<method>` with `_x_*` query params (`slack_route=T_EXAMPLE_TEAM`, `_x_csid`, `_x_id`, `_x_version_ts`, `_x_gantry=true`, `fp`, `_x_num_retries`, `_x_foreground`, `_x_frontend_build_type`, `_x_desktop_ia`). Token + `d` cookie ride in the body/headers (not in the URL). Status 200 on all.

Note: the extension reader returns URL + method + status only, not request/response bodies. For exact request params and response shapes on the AI endpoints, a targeted HAR of those specific calls is the follow-up.

## Native Slack AI (confirmed)

- `search.inline` — the inline "AI answer" / Ask endpoint. Confirmed: running a question query rendered an "AI answer" card (synthesized answer + citations + "Read more"). This is the `ai ask` backend.
- `ai.alpha.digest.listDigestChannelIds` — Recap/digest: lists channels with a digest ready. The Recap view rendered per-channel summaries ("5 recap channels have summaries ready to roll", "saved ~11 minutes catching up on 96 messages in 5 channels"). Namespace is `ai.alpha.*`.

## Search surface (full, captured from one query)

- Typeahead: `search.autocomplete`, `search.autocomplete.model`, `search.autocomplete.intentModel`, `search.autocomplete.offlineFeatures`, `search.autocomplete.files`, `search.autocomplete.topEngagedFiles`, `search.autocomplete.triggers`
- `search.precache`, `search.save`
- Result modules (fired in parallel): `search.modules.messages`, `search.modules.dms`, `search.modules.files`, `search.modules.channels`, `search.modules.people`, `search.modules.workObjects`
- `search.inline` (AI answer, see above)
- `messages.list` (xN) — message previews for results

## Other internal/standard methods observed (host + format confirmed)

- `conversations.history`, `conversations.suggestions`, `conversations.bulkReacjiTriggers`
- `client.counts` — unread state
- `users.interactions.list`, `users.interactions.set`
- `megaphone.notifications.list`, `dnd.teamInfo`, `team.info`, `helpdesk.get`, `apps.profile.get`

## Slackbot AI assistant (the user's "Slackbot AI", confirmed)

The Slackbot assistant in the right rail. Asking it a question fired, in order:
- `slackbot.precache` — warmup
- `drafts.create` — drafts the user's message
- `chat.postMessage` — the question is POSTED as a real message into the Slackbot conversation
- `conversations.replies` + `messages.list` (xN) — poll for the assistant's streamed reply
- `ai.alpha.slackbot.skills.listEnabled` — Slackbot AI's enabled "skills"
- `bots.info`, `subscriptions.thread.get` — bot metadata + thread subscription

Confirmed working: asked "What are the most important things I missed today?" and Slackbot replied in-thread with a prioritized digest (Very High / High / Good to Know + a top action item). So the assistant can answer, summarize, and recap conversationally.

Interaction model for the CLI (robust, all documented methods):
1. `chat.postMessage` the question into the Slackbot conversation (DM/IM with the Slackbot bot).
2. Poll `conversations.replies` (thread) until the assistant's reply finishes streaming.
3. Print the reply (text + any structured blocks).

## Revised AI architecture (from discovery)

- `ai ask "<question>"` (headline) -> route through the Slackbot AI assistant (post + poll replies). Robust, documented, and can answer/summarize/recap anything. This is the strongest AI command and does not depend on undocumented endpoints.
- `ai search "<query>"` -> `search.inline` for the inline AI answer card (undocumented but simple; bonus on top of normal search).
- `ai recap [--since]` -> either the Slackbot "what did I miss" prompt (robust) or `ai.alpha.digest.*` (undocumented). Prefer the Slackbot path for reliability; offer digest as an enhancement.
- `ai skills` -> `ai.alpha.slackbot.skills.listEnabled` to show what the assistant can do.

## Optional follow-up (max fidelity)

The extension reader gives method names but not bodies. A targeted HAR of `search.inline` (and one Slackbot exchange) would pin down exact request params + response shapes. Not required for the Slackbot-assistant path (documented methods); useful for `ai search` via `search.inline`.
