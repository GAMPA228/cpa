# Plugin Host Deployment

The plugin runtime is already part of the server. The management center's **Plugins** page
lists installed plugins, opens their registered resource menus, manages configuration
and per-plugin switches, and installs plugins from configured store sources. Existing
request-header and Turn State rules remain native and unchanged.

## Build for Linux

Go plugins need a Linux binary built with cgo and a working C toolchain. On the target
Linux architecture (amd64 or arm64), run:

```sh
CGO_ENABLED=1 go build -buildvcs=false -o cpa-server ./cmd/server
go version -m ./cpa-server  # Confirm CGO_ENABLED=1 in build settings
```

Use the same Go version, module dependencies, OS, and architecture when building
third-party `.so` plugins; Go rejects incompatible plugin ABI. The official release
workflow already builds its manylinux targets with cgo, but older custom packages
built with `CGO_ENABLED=0` cannot load plugins. Back up the previous executable and
configuration before replacing the server, then restart it. No database migration is
required. Restore the previous binary/config to roll back.

## Enable and Check

`plugins.enabled` defaults to `false` in `config.example.yaml`; `plugins.dir`
defaults to `plugins`. After deploying a cgo-enabled server and the matching
`management.html`, open **Plugins**, confirm that the loader is available, then
enable the global switch and install/enable individual plugins. A disabled global
switch leaves all plugin hooks inactive. The page uses authenticated
`/v0/management/plugins` APIs; a plugin's browser resources are served under
`/v0/resource/plugins/<id>/`. Only install plugins from trusted sources:
server-side Go plugins run in the server process and their web resources share its
origin. Verify a plugin by checking its registered/running state and exercising its
actual route or hook, not merely by seeing it in the store.

When deploying a locally built `management.html`, set
`remote-management.disable-auto-update-panel: true` unless your configured panel
repository publishes this customized frontend; otherwise the panel updater may
replace the file with a release that lacks the Plugins page.
