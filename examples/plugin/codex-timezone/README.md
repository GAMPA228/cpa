# Codex Request Timezone Plugin

Standalone C ABI plugin (`codex-timezone`, ABI 1, RPC schema 6). No CPA server
source changes are required. The plugin provides
its own **Request Timezone** resource menu. It starts disabled and preserves
requests until explicitly enabled in its settings page.

## Install on Linux AMD64

Use a plugin-enabled CPA build (the customized v4.0.24 is supported). Verify the
provided SHA-256 checksum, then place the `.so` in the configured plugin directory as
`codex-timezone.so`. Enable the global plugin system and this plugin. If installing
by copying a file, restart CPA to discover it; do not replace a loaded library in
place. Existing plugin entries and configuration must be preserved.

Version 0.1.2 changes only the plugin's timezone picker. Replace the library while
CPA is stopped, restart CPA, and refresh the browser. Existing rules are preserved.
The management frontend shipped with v0.1.1 continues to support automatic login;
this release does not include or require another frontend update. The host binary
and YAML do not change. The trusted same-origin iframe exchanges a nonce-bound handshake with
its parent. Both sides check the origin and source window; the parent also checks
the exact panel URL and an explicit plugin allowlist. No other plugin receives keys.
Older management pages and standalone tabs retain the **management key** input.
The plugin keeps the key in page memory, sends it only to authenticated management
API routes, and never saves it or puts it in URLs. Parent logout, page exit and
401/403 responses clear the session. Stale responses cannot restore a logged-out
session. Static HTML is public; account information and
all mutations are protected by the host's management authentication.

Settings are stored at `plugins/data/codex-timezone.json` relative to CPA's working
directory. The service user must own the writable data directory. Do not use
`chmod 777`. To override the path, add `data_file` to this plugin's own config via
the Plugins page; do not create a second top-level `plugins` section in YAML.

## Modes

- Keep: do not rewrite.
- Manual: use a validated IANA timezone.
- Automatic: look up the outgoing IP's timezone through `https://ipwho.is/`.
- Account rules override the default. Inherit follows the default.

The manual picker opens the complete browser timezone catalog, marks the current
selection, and provides a separate search field and scrollable list. Reopening
clears the search without changing the selected value. Valid browser-recognized
IANA aliases can also be entered in the search and selected; the backend remains
the final validator. Both default and per-account rules use the same picker.

For automatic mode, a Codex OAuth account's physical auth JSON `proxy_url` wins;
`direct`/`none` bypasses proxies. Otherwise the host's `host.http.do` callback uses
the current global proxy/default transport. Explicit proxies use the same SDK
HTTP/SOCKS transport builder as CPA. Invalid account proxies do not fall back to
direct. Auth material, proxy credentials and request bodies are never sent to the
geolocation service or exposed in the panel. The service sees the egress IP.

Account proxy configuration is synchronized every minute. Successful lookup
results are shared by effective route and cached in memory for six hours. Failed
lookups retry after five minutes; a manual refresh bypasses this delay. On failure
the last successful result is retained with an error indicator; without a result,
requests remain unchanged. Restarting clears the IP cache, not settings. After
changing a proxy, use **Refresh Egress** before testing. Queries run in a background
worker; the model request path does not perform network or disk IO.
Periodic work is suspended after two minutes without plugin requests or panel
activity. The next request/panel visit schedules a refresh without blocking it.

## Scope and Limits

- Rewrites existing user environment blocks and existing web-search timezone
  fields, preserving dates, other body fields, headers and model policies.
- Supports Codex-targeted HTTP (Responses/Chat input) and standard serial
  WebSocket create/append requests. Keep `codex.response-steering: false` (default):
  that optional duplex fast path bypasses after-auth hooks for follow-up frames.
  The plugin does not change that server setting.
- Missing, malformed or nested environment blocks are left unchanged. XML-like
  blocks with unescaped XML characters are conservatively skipped.
- Automatic detection requires physical Codex auth files readable through
  `host.auth.get`. Runtime-only API-key credentials are not automatically probed.
- Execution-scoped proxy overrides from other plugins are not exposed by this
  hook. Do not use automatic mode for those requests; use a manual rule instead.
- IP geolocation can be inaccurate. A rotating proxy or destination-based proxy
  routing can send the lookup service and OpenAI through different IPs.
- The global route inherits the host's existing transport and fallback behavior.
  This plugin cannot independently validate the global proxy configuration.
- No upstream model API was called during development. Real proxy/geo-service
  availability must be checked on the deployment server.

## Build and Test

From `go/` with Go 1.26 and a Linux C compiler:

```sh
go test -race ./...
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath \
  -buildmode=c-shared -ldflags='-s -w' -o codex-timezone.so .
```

This is a C shared library, not Go's `-buildmode=plugin`; it communicates through
the versioned C/JSON ABI and does not require matching Go package build hashes.
The target still needs a compatible libc and host ABI. Use `smoke.py` to test the
native ABI with fake accounts and fake IP responses; it makes no external calls.

To roll back, disable the plugin in the Plugins page. To remove it completely,
stop CPA, remove only this plugin's `.so`, then start CPA. Retain the settings file
for restoration. Disabling the plugin does not remove your rules.
