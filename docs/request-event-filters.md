# Request Event Filters

The request-event table supports combined result and local date/time filters.
Choose All, Success or Failed, enter either or both datetime bounds (to the
second), and select Query. Editing these controls does not change the applied
query until submission. Applying filters resets pagination; clearing filters
also clears result and time bounds.

The UI interprets timestamps in the browser's local timezone, matching the
displayed event times. The selected ending second is included in full. For
example, 18:30:00 through 18:35:00 becomes an interval starting at 18:30:00 and
ending immediately before 18:35:01, converted to UTC before transmission.

## API

`GET /v0/management/usage/details` accepts these optional parameters alongside
the existing model, API-key, source, account-index, search and pagination fields:

| Parameter | Meaning |
| --- | --- |
| `result` | `all` (or omitted), `success`, or `failed` |
| `start_time` | Inclusive RFC3339 timestamp with timezone |
| `end_time` | Exclusive RFC3339 timestamp with timezone |

Invalid timestamps, unsupported results, reversed/empty bounded intervals, and
timestamps outside SQLite's signed nanosecond range return HTTP 400.
Rows, total count and pagination all use the same filters. The backend memory
fallback uses the same interval and result semantics. CSV/JSON table exports
keep their existing loaded-row scope, restricted by applied filters.

## Deployment and Verification

Update both the server binary and `static/management.html`; older servers do not
apply the new query parameters. Existing data needs no backfill. Startup adds
`usage_details_failed_time_idx` without changing historical rows; the first
startup may take longer while the index is built. Older binaries can still use
the database, but will ignore the new filters.

Backend tests cover SQLite and memory filtering, timezone offsets, fractional
second boundaries, combinations with existing filters, pagination totals,
invalid inputs, existing-row index creation and indexed query planning.
