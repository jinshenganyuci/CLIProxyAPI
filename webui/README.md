# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.23.0`
- commit: `c98751140127a39985df565b827eec0c20d131d3`
- patch: `management-center-v1.23.0-credential-identity.patch`

The patch also adds independent Codex and Devin login proxy selectors. Proxy credentials are sent in the
POST body and held only in page memory; successful login and cancellation clear
the input. The selected egress is saved with the credential for later use. Native Devin OAuth
callback/cancellation, quota refresh, reset timelines, and cooldown views are
retained alongside the fork's Codex identity and reauthentication controls. Devin callback
guidance uses the v7.3.2 HTTP loopback `/callback` URL; validation remains compatible
with callbacks from earlier releases.

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout c98751140127a39985df565b827eec0c20d131d3
git -C management-center apply --unidiff-zero ../webui/management-center-v1.23.0-credential-identity.patch
docker run --rm --cpus 2 --memory 2g -e VERSION=v1.23.0 \
  -v "$PWD/management-center:/app" -w /app oven/bun:1.3.14 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
8f85c2a966da35a2301b43016ffb2c2972d71457397de091a7050e9c9463433f
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
