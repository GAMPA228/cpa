# Verification

## Scope

Only `examples/plugin/codex-timezone/` is added. No host API, executor, frontend
management application, production config, auth files, or existing plugins change.
The plugin uses the existing C ABI, after-auth interceptor, management routes,
`host.auth.list/get` and `host.http.do` callbacks.

## Checks

- Windows Go tests: rewriting, malformed inputs, account isolation, unchanged
  headers/model/reasoning/service tier, cache TTL, retry backoff, route changes,
  credential redaction, protected route boundaries and settings persistence.
- Linux `go test -race ./...` and native C ABI smoke test: registration, resources,
  persistence, fake host callbacks, auto lookup, body modification, disable/quiesce.
- Real customized CPA v4.0.24 binary: loads the `.so`, registers its menu, returns
  401 without the management key, saves settings and discovers a fake OAuth file.
- Isolated real-host forwarding test: a local HTTP/2 TLS server impersonates the
  upstream only inside a Docker container with networking disabled. Fake IP lookup
  and HTTP CONNECT proxies verify direct, inherited global proxy and per-account
  proxy detection. The actual forwarded body contains the selected timezone and
  preserves the original date. No real account/model/geo API is contacted.
- Playwright with mocked management endpoints: invalid/valid login, default and
  per-account modes, save, search, refresh, logout, desktop and narrow layout.
  Browsers close at the end; no frontend dev server is started.
- Root `go build -buildvcs=false -o test-output ./cmd/server` succeeds. VCS stamping
  was disabled because the sandbox user differs from the repository owner.

## Deployment Limits

Linux AMD64, C ABI 1, schema 6, glibc 2.34+. A real deployment must verify access to
the geo service and its terms. Rotating/destination-based proxies cannot guarantee
that a lookup and a model request use the same IP. Runtime-only credentials and
execution-scoped proxy overrides are not supported by automatic mode. Keep the
optional duplex response-steering path disabled; it bypasses subsequent after-auth
hooks. Details and rollback instructions are in `README_CN.md` / `README.md`.

## Indexing

The main repository's memory index excludes `examples/`. The plugin is indexed
separately; source reads, unit tests and real-host tests remain the source of truth.
