# Temporary Upstream Capture

## Operation

In **Usage Statistics > Request Events**, choose 10, 20, or 30 seconds (default
10), then enable **Capture upstream**. The server closes the admission window
after the selected duration, even
if the browser closes. Requests already admitted keep recording until EOF,
a terminal WebSocket event, an error, or the five-minute capture limit.
Turning the switch off stops admission, not the underlying requests.
Usage statistics must be enabled; no YAML setting is required.

The duration selector is locked during capture. Stop capture before changing
the duration. The management PUT accepts optional `duration_seconds` (10, 20,
or 30); omitting it preserves the previous 10-second default. Re-enabling an
already active capture does not extend its deadline. Status returns the
actual active duration and deadline; the selection is not persisted in YAML.

Open the capture icon on a usage row to inspect individual account/retry
attempts, request headers, request bodies, response headers, trailers, and response bodies.
JSON is formatted; SSE and Codex WebSocket events retain their order.
Downloads preserve captured HTTP bytes; WebSocket downloads contain ordered
message payloads as NDJSON. The preview/copy is limited to 256K characters.
Historical rows without a capture ID cannot be recovered retroactively.

## Fidelity And Limits

Capture runs before executor identity/model response rewriting, independently
of ordinary request logging. HTTP request bodies are copied as the transport
reads them. Request headers reflect the final application-level request after
account/model header rules. Credential-bearing request headers (including
Authorization, cookies, and API keys) are redacted before storage; repeated
values are preserved. Response headers, including Set-Cookie, retain their
existing unredacted behavior. URL credentials and query parameters are omitted.

This is application-layer diagnostics, not a packet sniffer. TLS, HTTP/2
framing, original header casing/order, and WebSocket control frames are not
stored. A transport may automatically decompress HTTP responses and remove
Content-Encoding/Content-Length; such responses are explicitly marked.
Reused Codex WebSocket connections show cached original handshake headers,
not newly computed headers that were never transmitted. Transport-generated
wire headers are not guaranteed to be present in the application-level snapshot.

Limits: 100 attempts per window, 2 MiB request body, 8 MiB response body,
64 MiB logical buffered payload, and 256 MiB logical persisted JSON.
Headers/trailers have a 256 KiB limit. Truncation, dropped captures, and
storage failures are surfaced; limits never cancel the actual API request.
Failed attempts that produce no usage event may not have a statistics row.

## Storage And Security

Payloads are written asynchronously to a separate SQLite file:
`<usage SQLite path>.captures.sqlite3`. Default:
`usagecompat.sqlite3.captures.sqlite3`. Quota accounting is unchanged.
Capture is always disabled after restart. Existing stores reopen for expiry
cleanup; capture data expires after 24 hours and is periodically deleted.
An offline service cannot perform cleanup. SQLite may retain allocated file
space after deletion, but secure_delete clears removed content.

Only authenticated management APIs can read, toggle, or delete captures.
Responses use Cache-Control: no-store. Active/saving captures cannot be
deleted. Payloads may include source code, prompts, personal data, cookies,
and upstream model names. Restrict filesystem access, use HTTPS, and exclude
the database and its journal from backups or Git unless explicitly required.

Deploy both the backend and rebuilt management.html. Existing statistics
receive an additive capture_id column; older rows remain valid. Reverting
the application does not require removing that column.

## Response Model Statistics

Request event details include a separate response_model field from the raw
upstream JSON or Responses events, before client-facing model rewriting.
This is the upstream-declared name, not proof of its internal implementation.
It is collected independently of the temporary capture switch. An early
response.created model survives interrupted streams; a later reported model
replaces it. HTTP, SSE, and Codex WebSocket execution paths are covered.

The usage_details table gains an additive response_model column, defaulting to
empty for historical rows. Missing values display as a dash, never a guessed
request model. Existing grouping, quotas, cost calculations, and deduplication
remain unchanged. Deploy both backend and management HTML; no YAML changes are
required. The extra column can remain when rolling back the application.

## Verification (2026-09-18)

Related diagnostics, usagecompat, executor helpers, Redis queue, and usage SDK
package suites passed, along with targeted HTTP/WebSocket response-model,
account-header and handshake-capture tests. Server compilation, frontend type
checking, scoped lint, production build, and desktop mocked UI checks passed.
The UI checks include column order, historical blanks, JSON/CSV exports, header
formatting/copy/download, and missing historical header snapshots.

The full executor suite is not green: tool-result input-modality expectations
and an Antigravity connection-pool count failed outside the edited paths; a clean
baseline comparison has not established their origin. A separate XAI positive
TTFT assertion failed once and passed on rerun. Race tests require unavailable
CGO/compiler support. No live upstream accounts were exercised.
