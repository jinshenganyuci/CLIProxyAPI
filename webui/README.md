# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.22.6`
- commit: `6586f88858ca27e840bd8db2630dccd371a1cd4a`
- patch: `management-center-v1.22.6-credential-identity.patch`

The patch also adds a Codex login proxy selector. Proxy credentials are sent in the
POST body and held only in page memory; successful login and cancellation clear
the input. The selected egress is saved with the credential for later use.

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout 6586f88858ca27e840bd8db2630dccd371a1cd4a
git -C management-center apply --unidiff-zero ../webui/management-center-v1.22.6-credential-identity.patch
docker run --rm -v "$PWD/management-center:/app" -w /app oven/bun:1.3.14 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
abbc27677a5b859aad8e1a57b30dab09ff97a7c7685c4d3052f9a6ad76146380
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
