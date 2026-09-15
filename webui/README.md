# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.23.1`
- commit: `c12997e1a544374336ea385d5e9a9bbabe1e4767`
- patch: `management-center-v1.23.1-credential-identity.patch`

The patch also adds independent Codex and Devin login proxy selectors. Proxy credentials are sent in the
POST body and held only in page memory; successful login and cancellation clear
the input. The selected egress is saved with the credential for later use. Native Devin OAuth
callback/cancellation, quota refresh, reset timelines, and cooldown views are
retained alongside the fork's Codex identity and reauthentication controls. Devin callback
guidance uses the v7.3.2 HTTP loopback `/callback` URL; validation remains compatible
with callbacks from earlier releases. The native v1.23.1 Devin quota flow queries
GetUserStatus through the management API with the selected credential index and a
body token placeholder, retaining request deduplication and stale-session guards.
It no longer falls back to old auth-file quota observations.

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
8ec0c6a7df3936216f0e50a076d2870a580cf3002d6f1950ccf3b8c318003f08
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
