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
retained alongside the fork's Codex identity and reauthentication controls.

Rebuild the asset from a clean checkout:

```sh
git clone https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git management-center
git -C management-center checkout c98751140127a39985df565b827eec0c20d131d3
git -C management-center apply ../webui/management-center-v1.23.0-credential-identity.patch
docker run --rm -v "$PWD/management-center:/app" -w /app oven/bun:1.3.14 \
  sh -c 'bun install --frozen-lockfile && bun run verify'
install -m 0644 management-center/dist/index.html static/management.html
sha256sum static/management.html
```

Expected SHA-256 for this source snapshot:

```text
e66bd437b9555b278bf02d92f5eb354a94e232766d15f2e47de6f8d31b4a9900
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
