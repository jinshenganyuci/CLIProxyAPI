# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.23.1`
- commit: `c12997e1a544374336ea385d5e9a9bbabe1e4767`
- patch: `management-center-v1.23.1-credential-identity.patch`

The patch also adds independent Codex, Devin, and Meta login proxy selectors. Proxy credentials are sent in the
POST body and held only in page memory; successful login and cancellation clear
the input. The selected egress is saved with the credential for later use. Native Devin OAuth
callback/cancellation, quota refresh, reset timelines, and cooldown views are
retained alongside the fork's Codex identity and reauthentication controls. Devin callback
guidance uses the v7.3.2 HTTP loopback `/callback` URL; validation remains compatible
with callbacks from earlier releases. The native v1.23.1 Devin quota flow queries
GetUserStatus through the management API with the selected credential index and a
body token placeholder, retaining request deduplication and stale-session guards.
It no longer falls back to old auth-file quota observations.

Meta device login displays the authorization URL, optional device code, expiry guidance,
and cancellation controls without a callback form. Its selected proxy is sent through
`POST /meta-auth-url` and stays locked until completion or cancellation. Browser
authorization uses the browser's own network settings. All four locales are included.
The official v1.23.1 UI has no dedicated Meta API-key editor; Meta API-key entries
remain editable through the native YAML configuration editor.

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

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout c12997e1a544374336ea385d5e9a9bbabe1e4767
git -C management-center apply --unidiff-zero ../webui/management-center-v1.23.1-credential-identity.patch
docker run --rm --cpus 2 --memory 2g -e VERSION=v1.23.1 \
  -v "$PWD/management-center:/app" -w /app oven/bun:1.3.14 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
a90d7eedc865e72618ec799f6b45ca7c454742922802eff90f0918b805fa0b61
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
