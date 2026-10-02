# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.25.2`
- commit: `752e0ee772220ce49aae1221a3f39f23236590d7`
- patch: `management-center-v1.25.2-credential-identity.patch`

The patch adds independent Codex, Devin, Meta, Kimi Code (kimi.com), and Kimi.ai
login proxy selectors. Proxy credentials are sent in the
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
authorization uses the browser's own network settings. All four locales are included.
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

Native regional login cards separate Kimi China (kimi.com) and Kimi International (kimi.ai) into
separate featured cards, each with its own registration link. The fork retains one card
per region and its existing POST proxy, cancellation, device-code, and expiry behavior.
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

The v1.25.2 UI uses `/v8/management` and the native nested configuration editor.
It supersedes the fork's temporary v0 visual-layout adapter. A configuration GET renders
legacy files as a v8 view without migrating the file; the first successful v8 configuration
mutation migrates persisted settings. Client keys under `access.api-keys` remain separate
from upstream groups under top-level `api-keys`. Provider writes preserve group policies,
null inheritance, unknown fields and credential row identity, with connection-switch guards.
Visual edits use targeted PATCH/DELETE/PUT operations. Recovery after partial writes does
not replay stale list indexes; conflicting list or payload replacements require a refresh.
Source YAML mode still replaces the entire supplied document. This is not an atomic
compare-and-swap guarantee against writes occurring after the current snapshot.

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

The v1.25.2 Claude quota card includes reset-grant status and a confirmation action for
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
false and apply across OAuth and API-key routes. The latter advertises the freeform
apply_patch capability only for models supported by their routing executors; enabling it
does not grant an unsupported upstream capability. Backend normalization migrates legacy
multi-agent paths before displaying the configuration. Explicit false values remain false,
and unrelated visual/provider saves preserve the client settings. Four locales and the
configuration search index use the same client scope. This bundled panel is verified with
its matching v8.0.10 backend.

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout 752e0ee772220ce49aae1221a3f39f23236590d7
git -C management-center apply --unidiff-zero ../webui/management-center-v1.25.2-credential-identity.patch
docker run --rm --cpus 2 --memory 2g -e VERSION=v1.25.2 \
  -v "$PWD/management-center:/app" -w /app oven/bun:1.3.14 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
fd34f7dbcfa46aff685857e9da7cb5797d9c556ef76c973748658c0e60b3ca14
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
