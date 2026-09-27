# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.24.2`
- commit: `4530da271ba2e89810d4dccebc57f3091afa590a`
- patch: `management-center-v1.24.2-credential-identity.patch`

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
`POST /meta-auth-url` and stays locked until completion or cancellation. Browser
authorization uses the browser's own network settings. All four locales are included.
The native Meta API-key editor is included. It preserves unknown backend configuration fields and
the latest per-credential concurrency limit when saving an older provider form. Meta quota
queries read the current DCA token from the selected auth file and use its auth index for
`POST /api-call`; the selected credential proxy remains authoritative. Only projected quota
fields enter the cache or UI, and stale session/file responses are discarded. Missing usage
is displayed as unknown. Usage-window and weekly reset timelines are included.

Kimi Code retains its original kimi.com endpoint and registration link. Kimi.ai has a
separate device-login card and endpoint; it does not replace existing Kimi Code accounts.
Both use POST JSON bodies for the selected proxy, distinguish inherited global settings
from explicit direct access, freeze proxy fields during the attempt, and clear secrets on
success or cancellation. Device codes and optional expiry are displayed without callback
forms. The native Kimi.ai backend has no dedicated quota adapter in this UI version, so
its accounts are not silently routed through the Kimi Code quota endpoint.

The native UI includes quota search by filename or email, Codex Business Premium entitlement
recognition and a live renewal-date lookup, Claude Team plan precedence, xAI billing-window
and unknown-usage corrections, plain credential-weight tooltips, and theme-aware plugin
resource backgrounds. Codex usage, renewal, and reset-credit requests all keep the selected
auth index through the management API. Renewal lookup is optional and falls back to the
credential date when unavailable. Search retains the shared concurrency polling context.

Native v1.24.2 merges Kimi China (kimi.com) and Kimi International (kimi.ai) into
separate featured cards, each with its own registration link. The fork retains one card
per region and its existing POST proxy, cancellation, device-code, and expiry behavior.
The quota search field and sorting now share a separate toolbar below provider navigation;
its clear action restores input focus. Provider tabs support vertical mouse-wheel scrolling
when horizontally overflowing, preserve horizontal gestures and zoom, and hide scrollbars.

Credential cards, compact rows, details, and quota cards share a small live concurrency badge
showing occupied requests and the effective limit. Each active page uses one lightweight
`GET /auth-files/concurrency` poll every two seconds, pauses while hidden, and discards
cancelled or stale-session responses. Failed or missing observations show unknown occupancy
instead of zero. Home-managed credentials display `— / Home` and disable local editing.

The credential editor accepts integer limits from 1 through 1,000,000. An empty field sends
`concurrency_limit: null` to restore the default of 10. Configuration-backed API-key credentials
use a concurrency-only editor, selected by the server's `concurrency_config` capability; this
editor writes the limit through the stable `auth_index` without downloading an auth file or
changing unrelated credential fields. Plugin virtual credentials retain their native controls.
Provider forms also preserve per-key and OpenAI-compatible fallback limits through normalization,
editing, and serialization. Saving an older provider form retains the latest stored concurrency
limit instead of overwriting a value changed through the credential card.

Provider model rows preserve the latest backend fields, including the native
`use-max-completion-tokens` setting. Exact upstream-name and effective-alias matches are
reserved before resolving a unique remaining alias edit. Reordering or deleting aliases
cannot move one model row's settings to another; ambiguous edits fail before a write. This
setting remains editable through the native YAML editor, with the backend default of false.

Model discovery for a selected OpenAI-compatible credential never retries without its auth
index after failure, preserving the configured credential proxy. Unbound public endpoints
retain their anonymous model-list fallback.

The v8 backend keeps the v0 JSON management contract used by provider forms.
The visual YAML editor additionally understands v8 nested paths and mixed legacy
layouts. It reads nested values by presence, including false, zero, and empty
values, then applies only edited fields to the latest YAML tree. Client keys under
`access.api-keys` cannot overwrite upstream groups under top-level `api-keys`.
Unchanged provider groups, credential proxies and concurrency settings, Codex
identity, plugin data, and unknown fields are preserved. Legacy-only files retain
their layout. The raw YAML editor still replaces the complete supplied document;
these visual edits do not provide atomic compare-and-swap against later writes.

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout 4530da271ba2e89810d4dccebc57f3091afa590a
git -C management-center apply --unidiff-zero ../webui/management-center-v1.24.2-credential-identity.patch
docker run --rm --cpus 2 --memory 2g -e VERSION=v1.24.2 \
  -v "$PWD/management-center:/app" -w /app oven/bun:1.3.14 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
84a0481ea0c8da5ac58c5efed7a348543cf326dbe6ba804e19338eed4d49f42c
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
