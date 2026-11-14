# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.28.0`
- commit: `3d893ec88938c3ceef200a15be6af8cf972564b3`
- patch: `management-center-v1.28.0-credential-identity.patch`

This release integrates official `v1.28.0` on top of the previously bundled `v1.27.3`,
while preserving native functionality and the fork controls described below.
The upgrade includes credential/quota card layouts, model catalog refresh, guarded
model-rule drafts, and login proxies. Login proxy controls use one dialog per attempt
with inherited, custom and direct modes; secrets stay in POST bodies and are cleared
with the attempt lifecycle. Plugin login entries use the same guarded controls.

The v8.0.59.2 fork adds dashboard byte statistics through the authenticated v8
`/observability/traffic` and `/observability/traffic/keys` endpoints. Container
interface receive/send bytes remain global; client payload bytes can be filtered
by a verified client-key identity. Neither measure is token usage or billing.
All-time, today and current-month cards accompany daily/monthly charts and exact
value tables. Detailed history covers the current and previous month in the
server's Asia/Shanghai timezone; cumulative totals remain independent of that window.

The paginated key directory includes zero-traffic and disabled keys, with source
and name/mask filtering and an option to include deleted keys. Native keys use
their verified credential identity; Access Guard keys use their stable plugin key
ID, including across rotation. Selecting a key only filters client payload data;
container network and unattributed traffic stay global. The page never consumes
the usage queue or reads plaintext keys for statistics.

Polling runs every five seconds only while the dashboard and document are visible.
Connection changes cancel pending reads and discard stale results. Disabled
collection retains history, memory-only operation keeps its counters with an
explicit warning, and unavailable data is distinct from zero. Collection, sample,
update and persistence timestamps are shown; the fifteen-second write interval
and possible loss of unpersisted data after an abrupt stop are explained in all
seven languages. This addition uses focused traffic tests, TypeScript/build
checks and desktop/mobile browser checks; it does not rerun the historical UI matrix.

The patch adds independent Codex, Devin, Meta, Kimi Code (kimi.com), and Kimi.ai
login-dialog proxy selectors. Proxy-capable tiles open the dialog before starting a
login so its egress can be selected first. Proxy credentials are sent in the
POST body and held only in page memory; successful login and cancellation clear
the input. The selected egress is saved with the credential for later use. Native Devin OAuth
callback/cancellation, quota refresh, reset timelines, and cooldown views are
retained alongside the fork's Codex identity and reauthentication controls. Devin callback
guidance uses the v7.3.2 HTTP loopback `/callback` URL; validation remains compatible
with callbacks from earlier releases. The native Devin quota flow queries
GetUserStatus through the management API with the selected credential index and a
body token placeholder, retaining request deduplication and stale-session guards.
It no longer falls back to old auth-file quota observations.

Meta device login displays the authorization URL, optional device code, expiry guidance,
and cancellation controls without a callback form. Its selected proxy is sent through
`POST /oauth/auth-url?provider=meta` and stays locked until completion or cancellation. Browser
authorization uses the browser's own network settings. All six locales are included.
The native Meta API-key editor is included. It preserves unknown backend configuration fields and
per-credential concurrency settings when saving a current provider form. Stale or ambiguous
provider snapshots are rejected before a write. Meta quota
queries read the current DCA token from the selected auth file and use its auth index for
`POST /requests/api-call`; the selected credential proxy remains authoritative. Only projected quota
fields enter the cache or UI, and stale session/file responses are discarded. Missing usage
is displayed as unknown. Usage-window and weekly reset timelines are included.

Kimi Code retains its original kimi.com endpoint and registration link. Kimi.ai has a
separate device-login card and provider parameter; it does not replace existing Kimi Code accounts.
Both use POST JSON bodies for the selected proxy, distinguish inherited global settings
from explicit direct access, freeze proxy fields during the attempt, and clear secrets on
success or cancellation. Device codes and optional expiry are displayed without callback
forms. The native quota adapter now recognizes both Kimi domains and monthly limits. Each refresh
reads the selected credential only in memory, follows the backend domain/base URL/type
precedence, and chooses an allowlisted kimi.com or kimi.ai usage endpoint. The request retains
the selected auth index and a token placeholder. Download/parse failures do not trigger a
guessed host or direct retry; connection and credential changes invalidate pending results.

The native UI includes quota search by filename or email, Codex Business Premium entitlement
recognition and a live renewal-date lookup, Claude Team plan precedence, xAI billing-window
and unknown-usage corrections, plain credential-weight tooltips, and theme-aware plugin
resource backgrounds. Codex usage, renewal, and reset-credit requests all keep the selected
auth index through the management API. Renewal lookup is optional and falls back to the
credential date when unavailable. Search retains the shared concurrency polling context.

Native regional login actions separate Kimi China (kimi.com) and Kimi International (kimi.ai),
each with its own registration link. The current shared sponsor card retains one independent
flow per region and its existing POST proxy, cancellation, device-code, and expiry behavior.
The quota search field and sorting now share a separate toolbar below provider navigation;
its clear action restores input focus. Provider tabs support vertical mouse-wheel scrolling
when horizontally overflowing, preserve horizontal gestures and zoom, and hide scrollbars.

Credential cards, compact rows, details, and quota cards share a small live concurrency badge
showing occupied requests and the effective limit. Each active page uses one lightweight
`GET /credentials/concurrency` poll every two seconds, pauses while hidden, and discards
cancelled or stale-session responses. Failed or missing observations show unknown occupancy
instead of zero. Home-managed credentials display `— / Home` and disable local editing.

The credential editor accepts integer limits from 1 through 1,000,000. An empty field sends
`concurrency_limit: null` to restore the default of 10. Configuration-backed API-key credentials
use a concurrency-only editor, selected by the server's `concurrency_config` capability; this
editor writes the limit through the stable `auth_index` without downloading an auth file or
changing unrelated credential fields. Plugin virtual credentials retain their native controls.
Provider forms also preserve per-key and OpenAI-compatible fallback limits through normalization,
editing, and serialization. An older provider snapshot is rejected if concurrent changes make
its persisted identity ambiguous or stale. Refreshing the form reads the current admission
limits; ordinary provider edits do not overwrite those limits.

Provider model rows preserve backend fields, including `use-max-completion-tokens`
and `support-configuration-update`, through the native persisted row identity. Reordering,
renaming, or deleting rows cannot move one row's settings to another. Invalid or repeated
row identities fail before a write. New rows retain their default capability settings.
The native advanced model editor now exposes these options, along with display names,
context lengths, supported modalities, compatibility behavior, and thinking budgets/levels.
A rejected stale snapshot must be refreshed before editing; the refreshed native form reads
the current options instead of treating those fields as opaque backend-only metadata.

Model discovery for a selected OpenAI-compatible credential never retries without its auth
index after failure, preserving the configured credential proxy. Unbound public endpoints
retain their anonymous model-list fallback.

The v1.26.2 UI uses `/v8/management` and the native nested configuration editor.
It supersedes the fork's temporary v0 visual-layout adapter. A configuration GET renders
legacy files as a v8 view without migrating the file; the first successful v8 configuration
mutation migrates persisted settings. Client keys under `access.api-keys` remain separate
from upstream groups under top-level `api-keys`. Provider writes preserve group policies,
null inheritance, unknown fields and credential row identity, with connection-switch guards.
Visual edits use targeted PATCH/DELETE/PUT operations. Recovery after partial writes does
not replay stale list indexes; conflicting list or payload replacements require a refresh.
Source YAML mode still replaces the entire supplied document. Configuration-page source/visual saves and provider-group writes bind the last read
ETag to their own save operation. Each successful visual mutation advances that
operation to the returned ETag; a 412 response stops it without an automatic retry.
Concurrent reads cannot replace another operation's ETag. A missing ETag after a
conditional write blocks subsequent mutations instead of guessing a revision.
Older v8 responses without ETags retain their existing behavior. Multiple mutations
remain non-transactional, and writers outside the backend lock are not covered.

Codex identity operations use `/credentials/codex-identity`, with initialization and rotation
subpaths. Login proxy passwords remain in POST JSON bodies; only the provider name is a
query parameter. Selected-credential model discovery never silently loses its auth index
on retry. Unbound discovery forwards its explicit proxy to the anonymous fallback.

The native cooldown-reset action, disabled credential styling, model-discovery merging,
and new logs workspace are included. Logs support wrapping, filters, scrolling buffers,
full-screen mode, and Escape events already handled by another component. The parser and
download request retain complete UUIDs as well as historical eight-hex-digit IDs. Local
filenames still use the final eight hex digits, and backend downloads select the latest
matching suffix: preserving a full ID in the UI does not make that lookup globally unique.

The credential editor includes request retry, model aliases, and request-scoped error
policies. These edits compose with the fork's narrow concurrency patch; configuration-backed
concurrency-only editors remain restricted to that field. Native single and bulk credential
refreshes show per-credential results and invalidate affected quota caches. Provider forms
expose runtime policy inheritance, explicit zero/false overrides, structured error rules, and
provider-specific request behavior without overwriting per-key proxy or concurrency settings.

Client API keys can have browser-local names and be searched by name. Name mappings are keyed
by a SHA-256 fingerprint scoped to the server and key, rather than persisting the original key
as the mapping identifier. This is local UI metadata, not a change to server authentication.
The native configuration editor also exposes OAuth behavior, Codex Live ICE servers, trusted
proxies and discovery settings, and preserves untouched duration fallback values and backend
YAML boolean semantics. Quota cards include Codex credit balances/unlimited credits and xAI
Grok subscription information. Autocomplete and shared advanced-field selects retain keyboard
and accessible-label behavior.

The v1.26.2 Claude quota card includes reset-grant status and a confirmation action for
eligible grants. Usage, organization-profile and redemption requests retain the selected
credential index through the management API and use a server-side token placeholder. The
selected credential proxy remains authoritative. Redemption rechecks eligibility and the
organization before sending; uncertain results require an explicit retry with the same
request ID. Expired uncertain operations remain blocked for the connection session. The
journal lives in tab memory and is neither a cross-tab nor durable spending ledger. The
existing local cooldown-reset controls remain available independently.

The fork corrects the native panel's old optimize-multi-agent-v2 field location to match
v8.0.9 and later backends. A separate Codex client compatibility group writes
`client.codex.optimize-multi-agent-v2` and `client.codex.enable-apply-patch`. Both default to
false and apply across OAuth and API-key routes. With enable-apply-patch disabled,
built-in Codex model templates retain their existing freeform apply_patch capability.
Enabling it extends capability advertisement according to actual routing support; it
does not grant unsupported upstream capability. Backend normalization migrates legacy
multi-agent paths before displaying the configuration. Explicit false values remain false,
and unrelated visual/provider saves preserve the client settings. Six locales and the
configuration search index use the same client scope. This bundled panel is verified with
its matching v8.0.37 backend.

The v8.0.12 adapter reads shared provider behavior from `upstream.codex`,
`upstream.claude` and `upstream.xai`, including Claude header defaults. Older OAuth
spellings remain readable as aliases; explicit canonical false/null values take
precedence. Editing an alias writes its canonical field and removes only the edited
legacy field. Unrelated OAuth settings, including Codex identity, credential proxy
policy and Live media relay, remain in their own scope. The search index and provider
behavior descriptions follow the same scope.

Provider configuration JSON now includes read-only `auth_index` values. Both underscore
and historical hyphen spellings are recognized for selected-credential operations.
Provider saves remove these fields only at group/key boundaries, and snapshot comparisons
ignore index-only changes. Actual changes to proxies, concurrency or nested headers still
reject stale edits. Same-named header and plugin fields remain ordinary configuration.
Provider writes also send the ETag from their own immediate configuration read.
They retain the existing identity/stale-form checks before sending that conditional write.

The retained v1.25.3 native update names Codex subscription tiers Pro 200 and Pro 100 in all
four locales while retaining their existing quota mappings. Plugin cards choose logos
from runtime metadata, then plugin metadata, then a uniquely matching store entry.
An unavailable plugin store does not block the installed-plugin list; store responses
from previous management connections are ignored.

The v8.0.15 backend accepts `models.catalog`, `models.codex-catalog`, and
`models.devin-catalog` in the YAML source editor. They accept empty strings, absolute
local paths, or HTTP(S) URLs. There are no dedicated visual controls for these sources.
Unrelated visual and provider edits preserve configured values; explicit empty strings
clear them through source editing. Catalog fetching and refresh policy belong to the
backend and are not implied by successful UI serialization.

The retained v1.25.4 native update adds Vietnamese, keeps Claude/Antigravity subscription
plan names untranslated in Chinese, and aligns shared-provider and Codex client
settings with their canonical `upstream.*` and `client.codex` paths. The fork already
used those canonical paths; its alias reads, explicit false/null precedence, and
narrow save guards are retained. Vietnamese also includes the fork's login proxy,
concurrency, and credential-identity controls, with current Devin callback and shared
provider/apply_patch guidance. Native Vietnamese interpolation checks cover the full
combined dictionary.

The retained v1.25.5 native update adds a masked `server.github-token` input. Targeted
visual edits replace or clear the token while preserving unrelated configuration and
plugin store authentication. The token remains visible to authorized configuration
reads and source editing; input masking is not encryption. Configuration content is
held in memory; configuration page storage retains display preferences, not this token.

Claude quota cards show remaining reset counts and each grant's expiration details,
including unknown expirations. Dollar-denominated legacy usage is labeled cloud
session credits separately from Claude Code quotas. All six locales include the new
labels. After confirmed Codex credit redemption, the UI clears the selected credential's
gateway cooldown before refreshing quota. Unconfirmed redemption, connection changes,
and mismatched clear acknowledgements do not trigger another redemption attempt.
All remote calls retain the selected auth index; local cooldown reset uses the same index.

Connectivity-test model selection is now transient. Provider saves remove obsolete
`test-model` only at known model/OpenAI-group locations and remove read-only auth indices
at credential/group boundaries. Same-named headers and opaque extension data, model
capabilities, proxy choices and concurrency limits remain intact.

The v1.26.2 native interface uses provider tiles and focused OAuth dialogs, including
sponsor tiles, device codes, callback feedback, and resumed status polling after an
accepted callback. The fork ports its proxy/direct/global choices into these dialogs
for Codex, Devin, Meta and both Kimi regions. Their proxy controls remain locked during
an attempt; closing the dialog, cancellation and success clear the secret input.
Closing preserves the selected mode and any ongoing login; it does not start or cancel
a server session. The OAuth close callback remains stable across parent updates, so
a notification disappearing during the native closing animation cannot reopen the
dialog. The callback still reads the latest committed flow and clears its secret draft. Reopening a custom proxy draft requires re-entering its URL before a
new login can start. Other providers retain
native immediate-start behavior. Korean includes the fork controls alongside the other
five locales.

Configuration sections retain the native setting-row layout and compact client-key
editor. The retired quota section is removed, and Antigravity credit options appear in
Advanced. The fork's two Codex client controls use the same setting-row components.
Unrelated edits preserve existing raw configuration, including opaque plugin data.

Generic plugin quota cards use the credential's backend-selected `quota_plugin_id`
for `POST /v8/management/plugins/:id/quota` with its exact `auth_index`. They do not
resolve a different plugin by provider name or retry through another plugin after
failure. Older v8 servers without this field retain the plugin-list compatibility
resolver. Same-file virtual credentials remain separate by their auth indices. These APIs are
present in the official v8.0.27 tag; no unreleased backend commits are required. Built-in
provider cards retain precedence. Probe-only credentials explicitly show unsupported
because this v8 route does not run declarative probes. Connection checks prevent pending
plugin-list results from issuing requests on a different management connection and
reject responses after a connection switch. xAI cards retain native shared-usage details
and the 1.0.46 client header.

The v8.0.24 backend adds `upstream.claude.tool-search` and the credential-only
`api-keys.claude[].keys[].native-tool-search`. Both default to false. The existing YAML
source editor can set these native fields; this UI baseline has no dedicated visual
switches for them. Unrelated visual edits and provider-form saves preserve true, false,
and omitted values without promoting the key-only flag to a provider group. The same
save path retains Vertex group `interactions` inheritance and explicit key overrides.

The matching v8.0.27 backend also accepts `server.max-request-body-bytes`,
`routing.session-affinity-rebind-on`,
`requests.streaming.send-keepalive-before-ft`, and
`oauth.providers.codex.signin-with-chatgpt-identifier`. The source editor supports
these fields. Unrelated visual/provider saves preserve explicit zero/false values,
custom identifiers and existing credential proxy/concurrency settings.

The v1.26.3 and v1.26.4 native changes consolidate the two Kimi regions into one
sponsor card with separate regional login and registration actions. Both routes
retain their own proxy choices, device-code flows and cancellation semantics.
Claude quota cards now show extra usage and independent dollar balances, retaining
the previous successful observation while refreshing or after a failed read.
Reset-grant card reads wait two seconds, share only concurrent requests for the same
credential and connection, and display rate limiting without automatic retries.
Spending still performs fresh eligibility and organization checks. The shared reader
retains the selected auth index and server-side token placeholder; it never replaces
the credential proxy with a page-level proxy or retries without a credential.

Native quota refresh actions account for reset operations. Hour-based window labels,
plan-less subscription display, xAI disabled pay-as-you-go display, quota amount
typography, and Chinese session-affinity terminology are included. The upstream
quota_plugin_id contract has converged with the fork's exact-plugin routing; the
fork additionally retains connection-switch guards and raw-field compatibility.

The v1.26.5 native update adds Gemini Argon to the Antigravity Gemini quota group,
updates its hub user-agent compatibility, translates Remaining bucket labels, and
corrects availability and refresh-action visibility. All five official commits are
included.

The v1.26.6 native interface adds a separate SIWC binding tile, a picker restricted
to backend-capable Codex files, locked targets during authorization, and binding
status/expiry on credential cards. Start sends only the selected filename; the
backend retains that credential's proxy policy, identity and concurrency settings.
Callbacks preserve the original loopback URL and require the active state and issued
client ID. Callback URL parameters cannot retarget the server-owned binding session.
An unsupported backend is displayed explicitly without ordinary-login fallback.

SIWC Close and Cancel both cancel the active binding session. This differs from
ordinary OAuth Close, which preserves a pending login. A late SIWC start response
after closing or switching connections is cancelled through its captured original
connection; stale responses cannot update the new connection or trigger its logout.
The fork stabilizes the SIWC close callback so notification-driven parent rerenders
cannot interrupt the native close animation. No native Modal or visual layout was
changed. The ordinary OAuth stable Close fix remains in place.

Native Claude quota cards now place reset-grant content after plan items. Reset
preflight failures use localized, locally generated stage/reason messages and never
show raw upstream error details. Unknown spending outcomes keep the existing
explicit-retry journal and selected credential binding.

The v1.26.7 native credential editor adds a Codex-only `use_siwc` toggle in all
six locales. Untouched and unchanged values produce no patch. Toggling sends only
the selected filename/auth index and explicit boolean; binding tokens, the outer
credential, proxy, identity namespace and concurrency limit remain unchanged.
Disabling preserves binding data for later re-enabling. The existing full editor
still downloads credential JSON; the narrow write is not a secret-free read path.
The backend's new SIWC status endpoint is separate and is not called by this editor.
Its local off/valid/invalid observation does not refresh or validate tokens online.

The fork stabilizes the credential editor's close callback so confirming a dirty
SIWC draft discard completes even when notification updates rerender the parent.
Save failures retain the draft with an explicit error. A late save from an old
management connection cannot close or update a newly opened editor. Generic Sheet
behavior and existing OAuth/SIWC dialog cancellation semantics are retained.

The v8.0.37 Vertex `openapi` and `openapi-location` key settings and model-level
explicit `openapi: false` can be edited through source YAML. Existing visual and
provider saves preserve these fields, credential proxy and concurrency settings;
this UI does not add dedicated Vertex controls for them.

The retained v1.27.2 native routing board, login redesign, auto-login guards, storage v2
migration, quota plan tiers, timeline corrections, Claude cooldown reset and HTTP
reset request IDs are included. Turkish now includes the fork's identity, credential
proxy, concurrency and client controls alongside all six existing locales.

The routing board's final configuration PATCH uses the ETag from its own fresh read.
A 412 stops the operation without an automatic retry. Credential changes completed
before a later configuration failure remain explicitly reported as applied; this is
not a transaction across credentials and configuration. Routing edits also verify
ordered key identities, including per-key proxy settings, before applying priority
or weight changes. Reordered compatible keys and equal API keys with different
proxies cannot silently receive each other's changes. Fresh unrelated pool and
concurrency settings remain intact.

The v8.0.49+ per-group/per-key `connection-pool` settings and
`client.claude.show-codex-commentary` can be edited in source YAML. Ordinary visual,
provider and routing saves preserve empty pool opt-in blocks, explicit false/zero
values, inheritance, selected proxy and local concurrency settings. This UI release
does not add dedicated visual controls for these backend settings.

The v1.27.3 native update includes the AIcodemirror multi-protocol provider,
Grok client version 1.0.70, and consistent quota-limit labels. Plugins may expose
multiple OAuth login entries, each retaining its own provider identifier, title,
icon, and attempt state. Entry metadata is decoded once from backend HTML escaping
and remains untrusted text rendered by React. Legacy single-entry plugin metadata
still works. Quota fallback can match any declared OAuth identifier; the server's
explicit quota plugin ID remains authoritative, with the fork's selected-auth-index
and connection-switch guards preserved.

The retained routing identity guard distinguishes keys, proxies, base URLs, prefixes,
and authentication indexes. Otherwise-identical duplicate credentials distinguished
only by model configuration retain the existing positional-write limitation.

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout 3d893ec88938c3ceef200a15be6af8cf972564b3
git -C management-center apply ../webui/management-center-v1.28.0-credential-identity.patch
docker run --rm -e VERSION=v1.28.0 \
  -v "$PWD/management-center:/app" -w /app oven/bun:1.4.3 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
bad9fd7ecbc9e6aee681bd6276d159e65823baf98c084858bc70c40d6ea2bc1e
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.

The traffic dashboard explains both client and upstream directions from CPA’s point of view.
The key directory hides deleted keys while retaining zero-usage and disabled keys.
Deleting a selected key returns to all-key totals, which retain historical deleted-key usage.
Container and client-payload measurements overlap and must not be added together.

The v8.0.59.5 dashboard adds combined receive-plus-send container traffic for
all-time, today, this month, and daily/monthly exact-value rows. It sums raw bytes
before formatting and keeps unknown values distinct from zero. Directional values
remain visible. Client payload cards and key rows keep their existing presentation;
collection, polling, persistence, and API contracts are unchanged.

Client API key notes now persist in server configuration and appear before masked keys.
Note edits use the existing configuration draft, preview and ETag save flow.
The traffic directory uses the same note without changing key identity or counters.
