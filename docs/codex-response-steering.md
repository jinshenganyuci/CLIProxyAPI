# Codex Responses WebSocket steering

Codex full-duplex Responses WebSocket steering is experimental and disabled by default.
Enable it through the existing configuration YAML editor:

```yaml
oauth:
  providers:
    codex:
      response-steering: true
```

Existing legacy `codex.response-steering` remains accepted. Successful v8 configuration
writes migrate it to the path above; read-only v8 configuration requests do not migrate
the persisted file.

The feature lets a client send `response.steer` while a response is running. The upstream
service decides whether to accept, reject, defer, or create a successor response. CPA
preserves upstream acknowledgements and does not fabricate acceptance. A connection stays
bound to one account, model, and upstream socket. Accepted input is never replayed on
another account or connection. Clients must handle an explicit connection closure.

The fork retains its credential proxy, identity, and concurrency requirements. Idle
connections do not consume a credential slot after their response finishes. Each forwarded
create requires a slot from the same credential limit shared with other API keys and HTTP
requests. A steering submission also reserves a slot before it can reach upstream, because
an accepted submission may create a successor without another client request. Failed or
pending steering releases its reservation; a successor consumes its reserved slot.

This reservation is visible in concurrency occupancy. At the configured limit, CPA rejects
the additional submission with a concurrency error and leaves the existing response
running. In particular, a credential limited to one request cannot accept a steering
submission while its parent response is still running. Raise that credential's limit if
this interaction is required. Unconfigured credentials retain the default limit of ten.

New creates and steering reservations also run the configured scheduling plugin with only
the already-bound credential as a candidate. This retains native-key binding, model and
quota checks without selecting a replacement account. Capacity refusal happens before
plugin admission; automatic successors reuse their existing reservation without another
scheduler/RPM charge. HTTP-upgrade authentication retains its existing lifecycle; this
does not add a new per-frame HTTP authentication mechanism.

Changing a bound credential's proxy, authentication material, or identity namespace prevents
further input from using the old socket. An inherited global proxy change is also checked.
The client must establish a new connection using the updated settings. CPA never
silently replays accepted steering or moves it to a different proxy. This setting governs
CPA's network traffic; external browser authorization still uses the browser's network.

Changes to unrelated global behavior flags are not promised to interrupt an existing socket.
The management UI does not add a separate steering switch. Use the YAML editor. Existing
configurations that omit this setting keep the ordinary WebSocket behavior.
