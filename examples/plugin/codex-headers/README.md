# Codex Headers Plugin

Standalone Linux Go c-shared plugin (schema 7). It handles account/model-scoped manual header rules, Turn State maintenance, and in-memory account-scoped HTTP cookies. Its three hooks run at the final Codex upstream transport boundary. The host and management UI must wire up these hooks separately; installing this plugin alone does not remove the native implementation.

## Build and Install

Build the server and plugin on the **same Linux architecture**, with the **same Go toolchain and module dependency versions**, and with cgo enabled:

```sh
CGO_ENABLED=1 go build -buildvcs=false -o cpa-server ./cmd/server
cd examples/plugin/codex-headers/go
CGO_ENABLED=1 go build -buildmode=c-shared -o ../codex-headers.so .
# Then install ../codex-headers.so as <server plugins.dir>/codex-headers.so.
```

Enable the host and the plugin in the server configuration:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codex-headers:
      enabled: true
      priority: 1
      # Optional: defaults to the same Turn State SQLite path used by usagecompat.
      # data_path: /absolute/path/to/usagecompat.sqlite3.turn-state.sqlite3
```

`data_path` is optional. Without it, the plugin uses `usagecompat.sqlite3.turn-state.sqlite3` in the server working directory, or `$USAGECOMPAT_SQLITE_PATH.turn-state.sqlite3` if that environment variable is set, exactly like the legacy implementation. An explicit `data_path` must be absolute. The manager creates a new SQLite file with private permissions when absent. To retain previous settings and history, point at the existing `*.turn-state.sqlite3` file (or omit the path when using the legacy default). **Keep a backup and do not write through the legacy Turn State settings endpoint while this plugin is active**; the frontend uses the plugin endpoint instead. The plugin persists Turn State rules/settings there; cookies remain in memory and vanish on restart. Only enable trusted plugins: they execute inside the server process.

## Settings

The plugin registers authenticated `GET` and `PUT /v0/management/plugins/codex-headers/turn-state`. GET returns the existing `enabled`, `max_chars`, `lifetime_seconds`, `account_scope`, `auth_ids`, `storage_errors`, and `dropped` shape. PUT requires `enabled` and `max_chars`; omitted lifetime and account scope fields retain their stored values. Account scope `selected` with `auth_ids` opts in individual OAuth accounts to automatic Turn State and cookies. Empty selections match no accounts; `all` opts in every Codex OAuth account. Use `enabled: false` to turn off automatic behavior without disabling manual header rules. The settings endpoint never returns header values or cookies.

`GET /v0/management/plugins/codex-headers/rules` returns `{"rules":[...]}` with account-scoped persisted automatic rules (`auth_id`, `id`, `model`, `value`, `issued_at`, `expires_at`, `active`, `account_enabled`, `duration_minutes`). Rule values are sensitive and are returned only through the authenticated management API. This listing enumerates account IDs from SQLite; serving model requests does not read the database. Account-scoped OAuth cookies remain active independently of the Turn State automation switch, as in the native implementation.

The plugin uses `request_header_rules` supplied by the host for each request; it does not store manual rules. Rule order follows `internal/authheaders`: model-specific overrides account-wide rules, expired entries are skipped, and default/delete/override retain native semantics. Refresh reservations are released through `codex.headers.complete`, including requests that never reached upstream.
The prepare response returns only changed/new headers and names of cleared headers. Cookie changes are reflected in a one-way WebSocket reuse signature; Cookie and Turn State values are never logged.

## Limits

- Requires a Linux cgo build; legacy static `CGO_ENABLED=0` server binaries cannot load `.so` plugins. Build and test the actual shared library on Linux before deployment.
- Go plugins cannot be unloaded from the running process. Disable/restart for a clean rollback; do not enable both native and plugin hook handlers simultaneously.
- Cookie Jar is scoped per OAuth auth ID and upstream HTTP(S)/WS(S) destination with public-suffix validation; WS(S) URLs are normalized to HTTP(S) for cookie matching. Secure cookies only travel on HTTPS/WSS. Cookies are not persisted.
- This plugin owns no management UI or manual-rule storage. It never logs sensitive header or cookie contents.
