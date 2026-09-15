# Devin OAuth for WebUI

Devin uses the existing management OAuth callback/status contract. No session token or PKCE verifier is returned to the frontend.

1. Request `POST /v0/management/devin-auth-url` with the management key and a JSON body. Select the credential's route before starting OAuth:

   ```http
   POST /v0/management/devin-auth-url
   Content-Type: application/json
   Authorization: Bearer <management-key>

   {"is_webui":true,"proxy_url":"socks5://127.0.0.1:1080"}
   ```

   The response includes `{"status":"ok","url":"https://app.devin.ai/auth/cli/continue?...","state":"..."}`. Omit `proxy_url` to select the current global route, or use `direct` to bypass global and environment proxies. Never put proxy credentials in a URL query. The native GET endpoint remains available for clients using the inherited route.
2. Open `url` in a browser and complete Devin authorization.
3. For a remote server, copy the complete final callback URL from the browser address bar (even if the loopback page cannot be reached), then submit it with the management key:

   ```http
   POST /v0/management/oauth-callback
   Content-Type: application/json
   Authorization: Bearer <management-key>

   {"provider":"devin","redirect_url":"<complete callback URL>"}
   ```

   Alternatively, submit `{"provider":"devin","state":"...","code":"..."}`. The `cognition` provider alias is also accepted. Keep the state from the original login attempt; do not mix concurrent attempts.
4. Poll `GET /v0/management/get-auth-status?state=...` with the management key. `status` is `wait`, `ok` after credentials are saved, or `error` with an `error` message. A successful callback submission alone does not mean token exchange has completed.
5. Cancel a pending attempt with `DELETE /v0/management/oauth-session?state=...`.

The generated redirect uses the server's configured port at `http://127.0.0.1:<port>/callback` (Devin's authorization page strictly validates that the redirect URI is `http://127.0.0.1:<port>/callback`). When the browser can reach that loopback server (local deployment or an appropriate tunnel), the public callback route accepts the redirect automatically. Remote deployments do not require opening an extra callback port: use step 3 instead. This is a browser authorization-code flow, not a device-code flow.

The legacy `/devin/callback` route remains available for pending or older clients. If the CPA listener itself uses TLS, submit the final loopback URL through the management callback API; the required HTTP redirect does not create another plaintext listener.

Authorization must finish within five minutes. Credentials use the same `devin-*.json` format as `--devin-login`, including normalized session tokens, OAuth auth kind, and best-effort profile/plan metadata. Account/quota enrichment failures do not invalidate an otherwise valid session token.

## Selected proxy lifecycle

The route is resolved and frozen when login starts. CPA's token exchange and profile/status requests use that route even if global configuration changes before the callback. Successful creation persists the route as the credential's `proxy_url`. Later inference and status refresh honor that explicit route; invalid settings fail instead of falling back to the default network. An independently opened browser uses its own networking configuration.

The callback directory and save destination are also bound to the login session. Reloading the configured auth directory while a browser is open does not move that pending callback. Saving rechecks cancellation and the latest credential controls under the shared file lock. An implicit relogin which would replace a different existing proxy fails with an instruction to select that proxy explicitly; an explicit proxy selection intentionally permits updating the route.

The bundled panel is built from management center v1.23.1 with this fork's additions. Its Devin card uses the POST endpoint and clears proxy input after successful login or cancellation. The Codex proxy, reauthentication, and credential identity controls are retained. Rebuild instructions and the exact upstream source version are in `webui/README.md`.

## Headless CLI login and panel quota calls

`--devin-login --no-browser` uses the upstream manual code flow without binding a
callback port. It accepts an authorization code, supported callback URL, or
session token. CPA freezes the selected transport before prompting; both the
headless and browser branches use the shared guarded credential storage.
The management WebUI continues to use the callback/state flow documented above.

The v1.23.1 panel fetches Devin quota through management `APICall` with the selected
`authIndex` and `metadata.apiKey` set to `$TOKEN$` in the JSON request body. Token
replacement and the request use the selected credential transport; the panel no
longer falls back to an auth-files quota snapshot.

If the explicitly selected `authIndex` no longer resolves, `APICall` returns HTTP
404 before creating an outbound transport or resolving a token. Refresh the
credential selection before retrying. Generic calls that omit the index remain
available under the existing management authentication rules.
