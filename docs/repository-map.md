# Repository Map

This backend project is maintained together with a separate frontend management-center project.

## Backend

- Local path: `F:\codex\CLIProxyAPI`
- User repository: `git@github.com:GAMPA228/cpa.git`
- User repository remote alias: `origin`
- Official repository: `git@github.com:router-for-me/CLIProxyAPI.git`
- Official repository remote alias: `upstream`

## Frontend Management Center

- Local path: `F:\gitRepository\Cli-Proxy-API-Management-Center`
- User fork: `git@github.com:GAMPA228/Cli-Proxy-API-Management-Center.git`
- User fork remote alias: `origin`
- Official repository: `git@github.com:router-for-me/Cli-Proxy-API-Management-Center.git`
- Official remote alias: `upstream`

## Official Update Workflow

When asked to pull the latest official updates:

1. Check the target repository worktree first.
2. Preserve uncommitted local changes.
3. Commit local changes before pulling when the user asks to do so, or when the update may conflict.
4. Pull backend official updates from backend `upstream`.
5. Pull frontend official updates from frontend `upstream`.
6. Do not push to any remote unless the user explicitly asks.
