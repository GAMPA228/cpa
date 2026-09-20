# Codex Account Request Headers

Configure account-specific rules in the **Request Header Rules** sidebar page.
The old Auth Files editor no longer edits these rules.
The existing `codex-header-defaults` configuration and unconfigured accounts retain
their current behavior. This feature does not change model rewriting, reasoning,
Fast policies, downstream API-key groups, or quota accounting.

Manual rules are stored in each authentication JSON file, not in `config.yaml`:

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

## Automatic Turn State Rules

The request-event toolbar has a separate **Automatically maintain Turn State**
switch and **Maximum characters** input. Save applies both settings. Defaults are
disabled and 292 characters; integer thresholds from 1 through 8192 are accepted.
This does not require diagnostic capture or an open browser page.

Fresh Codex OAuth responses are observed using the selected account ID and actual
upstream request model, after model rewriting and alias resolution. A nonempty
`X-Codex-Turn-State` no longer than the threshold must also have a valid public
Base64URL/Fernet structure and a plausible issue timestamp. This validates neither
the signature nor the meaning of the encrypted state. A 60-minute deadline from
the issue timestamp is a local policy, not an upstream lifetime guarantee.

Each account/model has at most one automatic rule. During its final five minutes,
one ordinary request is reserved to refresh it with `X-Codex-Turn-State` removed,
including client-supplied copies of the header. Other concurrent requests retain
the old rule while it is valid. The reservation lasts until the request finishes
(including streaming WebSocket metadata); unsuccessful attempts have a 30-second
cooldown before another ordinary request can refresh. A qualifying response with
a strictly newer issue timestamp replaces the rule only after successful storage.
There are no synthetic requests or guaranteed renewals: absent traffic or a fresh
qualifying response, the old rule expires. Repeated old values cannot extend it.
Expired or oversized managed values are not sent, and client headers are removed
for those managed account/model pairs until a usable replacement is available.
Pairs without a previously observed automatic rule retain normal header handling.
Lowering the threshold immediately suppresses oversized automatic rules; turning
the switch off suppresses all automatic rules while retaining their records.
Manual rules remain independently managed. Automatic entries in Request Header
Rules are marked with their source and are read-only; the protected header is not
opened to arbitrary manual editing.

Settings and automatic values are stored in `<usage SQLite path>.turn-state.sqlite3`,
with owner-only file permissions where supported. Existing files are loaded at
startup; per-request matching is memory-only. Rule changes use a bounded background
queue and become active after successful persistence. Shutdown drains the queue
within the service's shutdown context; cancellation skips remaining observations.
At most 128 automatic models are retained per account; expired entries are reclaimed
when a new model needs space.
Back up this sensitive file with the other private runtime data; do not commit it.
No `config.yaml` changes are required. Settings endpoints are administrator-only
GET/PUT `/v0/management/usage/turn-state-auto-rules` (`enabled`, `max_chars`).

Usage details expose nullable `turn_state_length`, including when automation and
capture are off. The number measures the trimmed Base64 header characters, not
decoded bytes or the injected request value. Missing, empty or ambiguous duplicate
values and historical records show `-`. Fresh WebSocket handshake/metadata headers
can supply the value; reused handshake snapshots cannot renew rules or supply a
new request's length. Changed effective rules replace a reusable connection on
its next use through the existing replay-required path.

A refresh uses a distinct WebSocket connection key to force a new handshake even
if an earlier headerless connection remains reusable. A continuation that requires
its current connection follows the existing replay-required path; no incremental
input is sent on a replacement socket without replay. This can require the client
to replay the request. A preflight replay does not start the refresh cooldown.
HTTP fallback releases the WebSocket reservation before trying the HTTP request.

Deploy the backend and management HTML together. SQLite adds one nullable usage
column automatically; older records are unchanged. Disabling automation restores
normal header handling. Older binaries ignore the extra column and separate store.
The proactive refresh fix is backend-only and requires no new configuration or
database migration; existing switch and character-threshold settings are retained.

## Verification

- Refresh tests cover the exact five-minute boundary, concurrent account/model
  reservations, cooldown, expiry, threshold changes, durable replacement failures,
  restart, header casing, and connection fingerprints using deterministic clocks.
- Automatic Turn State tests cover threshold validation, public envelope parsing,
  account/model isolation, renewal boundaries, stale observations, concurrent updates,
  restart recovery, write failures, authenticated settings routes, read-only rule
  views, and nullable usage migrations/import/export. Mock HTTP and WebSocket
  requests verify response observation, subsequent header injection, disabling,
  connection replacement, and omission of reused-handshake lengths.
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
