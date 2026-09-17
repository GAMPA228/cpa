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
attempts, request bodies, response headers, trailers, and response bodies.
JSON is formatted; SSE and Codex WebSocket events retain their order.
Downloads preserve captured HTTP bytes; WebSocket downloads contain ordered
message payloads as NDJSON. The preview/copy is limited to 256K characters.
Historical rows without a capture ID cannot be recovered retroactively.

## Fidelity And Limits

Capture runs before executor identity/model response rewriting, independently
of ordinary request logging. HTTP request bodies are copied as the transport
reads them. Header values, including repeated Set-Cookie, are not redacted.
Authorization request headers are not collected. URL credentials and query
parameters are omitted.

This is application-layer diagnostics, not a packet sniffer. TLS, HTTP/2
framing, original header casing/order, and WebSocket control frames are not
stored. A transport may automatically decompress HTTP responses and remove
Content-Encoding/Content-Length; such responses are explicitly marked.
Reused Codex WebSocket connections show cached original handshake headers.

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
