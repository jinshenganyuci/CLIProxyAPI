# Bundled management UI

The image serves `static/management.html` at `/management.html`. It is built from:

- repository: `router-for-me/Cli-Proxy-API-Management-Center`
- tag: `v1.22.6`
- commit: `6586f88858ca27e840bd8db2630dccd371a1cd4a`
- patch: `management-center-v1.22.6-credential-identity.patch`

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
038080a6d2c41e0588454db94990d935576e257b09d6d6d144c8fa4a1b1bdb80
```

The Docker image sets `MANAGEMENT_STATIC_IMMUTABLE=true` so the upstream panel
auto-updater cannot overwrite the matching bundled UI at runtime.
