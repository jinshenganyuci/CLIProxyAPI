# Credential concurrency

Standalone CPA limits each credential to **10 active requests by default**. The limit
is shared across downstream API keys, models, and sessions using that credential
inside the same CPA process. Adding another downstream key does not add capacity.
Separate CPA instances do not share these local counters.

## Configure a credential

Open **Auth Files**, edit a credential, and set its concurrency limit. Enter `3` to
allow at most three active requests for that credential. Clearing the field restores
the default of 10. Valid explicit limits are integers from 1 through 1,000,000;
zero does not disable the limiter.

The compact badge displays active requests and the effective limit, for example
`3 / 10`. Visible pages refresh the lightweight counter endpoint every two seconds.
An unavailable observation is shown as unknown rather than as zero.

For JSON credentials, the optional top-level field is:

```json
{
  "concurrency_limit": 3
}
```

This is a field to add to the existing credential, not a complete auth file. Config
API-key credentials use `concurrency-limit` on their key entry instead:

```yaml
codex-api-key:
  - api-key: "example-only"
    concurrency-limit: 3
```

OpenAI-compatible providers use the field on each `api-key-entries` entry. A provider
with no key entries can set the field at provider level. Changing a limit preserves
the credential's ID, proxy, tokens, and downstream key bindings.

The management API supports:

```http
PATCH /v0/management/auth-files/fields
Content-Type: application/json

{"name":"credential.json","concurrency_limit":3}
```

An optional `auth_index` verifies the exact selected credential. Set
`concurrency_limit` to `null` to restore the default. Config-derived credentials
must update this field separately from other metadata; CPA saves their underlying
configuration and reloads it. Plugin-expanded virtual credentials retain their
existing restrictions on independent metadata editing.

`GET /v0/management/auth-files/concurrency` returns management-authenticated
`entries` containing `id`, `name`, `auth_index`, `concurrency_limit`,
`concurrency_in_flight`, and `concurrency_managed`. The endpoint reads local runtime
state without fetching provider quotas or invoking plugins. Runtime occupancy is
never persisted to the credential file.

## Admission and release

- Known-full credentials are excluded before scheduling. Existing model, priority,
  session, and plugin authorization rules still apply to the remaining candidates.
- When no permitted capacity is available, CPA returns HTTP 429 with the code
  `credential_concurrency_exceeded`. Requests are not placed in an unbounded queue.
- A local capacity rejection does not mark the credential as quota-exhausted or
  place it into an upstream rate-limit cooldown.
- Streaming requests retain their slot until execution completes or cancellation
  stops the stream. Responses WebSocket requests share these same credential slots.
- Request failures and retries release the previous slot before using another.
  Provider token refresh, quota inspection, and model discovery do not take an
  inference slot. Token-count requests that use the execution manager do take a slot.
- Reducing a limit does not interrupt requests already running. A badge may briefly
  show `3 / 2`; no additional request is admitted until occupancy is below the new
  limit. Increasing a limit makes the additional capacity available immediately.
- Removing, updating, or re-registering the same credential ID cannot reset active
  occupancy. Restarting CPA starts new counters after its previous process ends.

## Access Guard compatibility

Capacity filtering preserves Access Guard's credential allow-list; CPA never falls
back outside the plugin's authorization to find a free slot. RPM remains a separate
per-minute policy.

Older scheduler plugin interfaces cannot reserve a slot atomically with plugin
selection. If another request takes the final slot during that interval, CPA returns
a local 429 instead of invoking the plugin repeatedly and charging RPM again. A
subsequent client request can retry. With this older interface, an `auth_not_found`
result after full candidates were filtered is reported as local capacity exhaustion;
explicit plugin permission and quota errors remain unchanged.

Existing plugin limitations for specialized routes remain in effect. This feature
does not add Access Guard binding support to routes that previously bypassed its
scheduler.

## Home and live media

Home mode keeps its authoritative concurrency policy. Local limits are not applied
on top of Home, editing is disabled, and the UI displays Home-managed state instead
of inventing local occupancy.

Codex Alpha Search and proxied Realtime connections also acquire credential slots.
A Live media session retains its slot beyond the initial SDP response and releases
it after its session resources and media have closed. Replacement, hangup, session
expiry, and handler shutdown use the same cleanup; a bootstrap completing after
handler shutdown cannot retain a new session. Sideband connections and hangup
controls reuse the existing session slot.

When WebRTC media goes directly between the browser and the provider, CPA cannot
immediately observe a media disconnect. It conservatively retains that session's
slot until explicit hangup, observed session closure, or the existing session expiry.
