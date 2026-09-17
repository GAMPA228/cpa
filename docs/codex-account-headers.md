# Codex Account Request Headers

Configure account-specific rules in **Auth Files > Edit > Account Request Headers**.
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
- Names are case-insensitive. Duplicate names, invalid control characters, more than
  32 rules, names over 128 bytes, and values over 8192 bytes are rejected.
- Credentials, account binding, transport framing and system-managed device/session
  headers are protected. `User-Agent`, `Version`, `Originator`, and
  `X-Codex-Beta-Features` may be customized.

Rules apply to Codex HTTP and WebSocket execution, including streaming and image
requests. They are read from runtime account metadata, without per-request disk I/O.
Retrying with another account uses that account's rules.
Changing or clearing rules replaces an old WebSocket connection on its next use;
an in-flight request is not interrupted. Connection-dependent continuation uses
the existing replay-required path rather than reusing stale headers.

Deploy both the updated backend and management HTML. No schema migration or new
global configuration is required. Removing `request_header_rules` restores the
previous behavior. Do not commit authentication files or real header secrets.

## Verification

- Targeted Go tests cover rule validation, account isolation, HTTP/WebSocket
  streaming and non-streaming requests, connection replacement, upload rejection,
  metadata reload, PATCH clearing, and existing header/single-device behavior.
- Backend compilation passed with `go build -buildvcs=false -o <temporary-path> ./cmd/server`.
  VCS stamping was disabled only for this compile check because of workspace ownership.
- Frontend type checking, scoped ESLint, and production build passed.
- Desktop browser checks covered editing, validation, save/reopen, removing all
  rules, account isolation, and hiding this editor for non-Codex accounts.
- Tests used local mock upstreams; no real account requests were made.
