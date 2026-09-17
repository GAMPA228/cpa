# Codex Account Request Headers

Configure account-specific rules in the **Request Header Rules** sidebar page.
The old Auth Files editor no longer edits these rules.
The existing `codex-header-defaults` configuration and unconfigured accounts retain
their current behavior. This feature does not change model rewriting, reasoning,
Fast policies, downstream API-key groups, or quota accounting.

Rules are stored in each authentication JSON file, not in `config.yaml`:

```json
{
  "type": "codex",
  "request_header_rules": [
    {"name": "User-Agent", "operation": "override", "value": "my-client/1.0"},
    {"name": "X-Example", "operation": "default", "value": "account-a"},
    {"name": "X-OpenAI-Internal-Codex-Responses-Lite", "operation": "delete"}
  ]
}
```

Merge the field into an existing authentication file; this example omits credentials.

- `override` replaces the outgoing value after existing default/model/identity processing.
- `default` adds a value only when the outgoing header is absent.
- `delete` suppresses the outgoing header. Removing a rule instead restores normal behavior.
- Names are case-insensitive. Overlapping header/model scopes, invalid control characters, more than
  32 rules, names over 128 bytes, and values over 8192 bytes are rejected.
- Credentials, account binding, transport framing and system-managed device/session
  headers are protected. `User-Agent`, `Version`, `Originator`, and
  `X-Codex-Beta-Features` may be customized.

Rules apply to Codex HTTP and WebSocket execution, including streaming and image
requests. They are read from runtime account metadata, without per-request disk I/O.
Retrying with another account uses that account's rules.
Model matching uses the actual upstream model, after rewriting and alias resolution.
An empty model list applies to all models. A matching model-specific rule takes
precedence over an account-wide rule with the same header name.
Changing effective rules, switching models, or reaching expiry replaces an old
WebSocket connection on its next use;
an in-flight request is not interrupted. Connection-dependent continuation uses
the existing replay-required path rather than reusing stale headers.

Deploy both the updated backend and management HTML. No schema migration or new
global configuration is required. Removing `request_header_rules` restores the
previous behavior. Do not commit authentication files or real header secrets.

## Temporary Rules

The duration selector offers permanent, 10, 20, 30, 40, 50, and 60 minutes.
Saving a new temporary rule creates a backend-generated UTC deadline. Changing
its duration or explicitly restarting its timer creates a new deadline.
Editing its value/models or another rule does not renew it, even after expiration.
Refreshes and server restarts preserve stored deadlines. Expired rules remain
visible but are excluded from subsequent requests. Other active rules or normal
defaults then apply; expiration does not necessarily remove the header entirely.

Legacy rules are interpreted as account-wide and permanent. Stable legacy IDs are
derived without writing during reads; editing persists IDs with the rules.
The authenticated management endpoints are GET and POST
`/v0/management/request-header-rules`. POST changes one rule with an account
revision token; stale edits return 409. Only rule fields are accepted, never OAuth
credentials or client-provided expiry timestamps. Concurrent token refresh data is
preserved by the existing metadata merge path.
Storage failure returns an error; runtime state may already reflect the edit, so
refresh before retrying. The UI does not report durable success in this case.

## Verification

- Targeted Go tests cover rule validation, account isolation, HTTP/WebSocket
  streaming and non-streaming requests, connection replacement, upload rejection,
  metadata reload, PATCH clearing, and existing header/single-device behavior.
- Backend compilation passed with `go build -buildvcs=false -o <temporary-path> ./cmd/server`.
  VCS stamping was disabled only for this compile check because of workspace ownership.
- Frontend type checking, scoped ESLint, and production build passed.
- Desktop browser checks covered editing, validation, save/reopen, removing all
  rules, account/model/status filters, all seven duration choices, explicit
  restart, stale edit conflicts, and legacy-rule display on the dedicated route.
- Deterministic clock tests cover exact expiry boundaries, model precedence,
  restart-safe deadlines, and edits that must not renew rules. Local HTTP and
  WebSocket integration tests cover model switching and expired rule removal.
- Tests used local mock upstreams; no real account requests were made.
