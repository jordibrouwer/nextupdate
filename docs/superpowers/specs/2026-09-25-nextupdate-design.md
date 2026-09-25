# nextupdate — design

Date: 2026-09-25
Status: approved design, pre-implementation

## Purpose

nextupdate is a self-hosted update cockpit for Docker containers, built as a companion to nextdash. It tells you *what* changes before you update (release notes between your version and the new one), flags risky updates as breaking, updates with one click or by per-container policy, and rolls back automatically when the new container fails its health checks.

It aims to be better than existing tools rather than new in kind:

- Watchtower updates blindly and has no UI or rollback.
- Diun and WUD notify that an update exists, but do not show what changed and do not roll back.

Audience: Unraid beginners and homelab tweakers. Own web UI (PWA with push) plus a summary widget in nextdash.

## Scope

**v0.1**

- Container sources: Docker Compose services and plain `docker run` containers on one host.
- Update detection by digest comparison (also for `:latest`), without pulling.
- Changelog per image from GitHub Releases, covering every release between the running and the available version.
- Breaking classification.
- Manual one-click update and automatic update per container policy.
- Automatic rollback on failed verification, plus manual rollback.
- Notifications: Web Push, and ntfy / Gotify / Discord / Telegram / e-mail through Shoutrrr-style URLs.
- nextdash widget endpoint.

**v0.2**

- Unraid template (dockerman XML) adapter.
- Optional appdata snapshot before an update.
- Shared community mapping repository (image → source repo, breaking flags).

**Later**

- Multiple hosts through lightweight agents.

**Out of scope for v0.1:** multiple hosts, remote Docker sockets, translations other than English, deploying new stacks.

## Architecture

One Go binary in one container. It listens on 8099 inside the container.

Mounts and configuration:

- `/var/run/docker.sock`, or `DOCKER_HOST` pointing at a docker-socket-proxy (recommended).
- `/data`: SQLite database and cached data.
- `/stacks`: compose project directories, read-write.

The image ships the Docker Compose CLI.

### Components

Each component has one purpose and a narrow interface so it can be tested with fakes.

| Component | Responsibility |
|---|---|
| `discovery` | Lists containers through the Docker API and determines the source: compose (via `com.docker.compose.project.working_dir` and service labels) or run. |
| `registry` | Compares local and remote digests with a manifest HEAD request against Docker Hub, GHCR, lscr.io and Quay. Handles auth tokens, 429 backoff and optional credentials. |
| `changelog` | Resolves image → repo (OCI label `org.opencontainers.image.source`, then community mapping, then manual entry). Fetches GitHub releases in the version range and caches them with ETags. |
| `classifier` | Marks an update breaking on a semver major jump, on keywords in release notes (`BREAKING`, `migration`, `deprecated`, …) or on a community flag. Records the reason. |
| `updater` | Interface `Plan`, `Apply`, `Verify`, `Rollback`. Adapters: `compose`, `run`; `unraid` in v0.2. |
| `verifier` | Decides success within a configurable window: Docker healthcheck, optional HTTP check (expects 2xx), crashloop detection (restart count). |
| `policy` | Per container: `notify`, `auto` (patch/minor only, never breaking), or `never`. Runs the check schedule. |
| `notify` | Web Push (VAPID) and Shoutrrr-style notifier URLs. |
| `api` + UI | Login, update list, changelog view, update and rollback actions, history, settings. Vanilla JS. |
| `widget` | `GET /api/widget` for a nextdash custom widget. |

### Update flow

1. Scheduled or manual check: `discovery` → `registry` finds a new digest.
2. `changelog` fetches notes; `classifier` sets the breaking flag.
3. `notify` sends a message according to policy.
4. Update starts (click or `auto` policy) → `updater.Apply`.
5. `verifier` checks the new container.
6. On failure → `updater.Rollback`.
7. Result goes to `history` and triggers a notification.

## Update engine (hybrid)

The compose file stays the source of truth; nextupdate never edits it.

### `run` adapter

1. Inspect the current container.
2. Pull the new image.
3. Stop the old container and rename it to `<name>-nu-old`.
4. Create and start the new container with the same config under the original name.
5. Verify.
6. Success: remove the old container. Keep the old image for N days (setting, default 7).
7. Failure: remove the new container, rename the old one back and start it. No pull needed.

### `compose` adapter

1. Record the current image ID.
2. `docker compose pull <service>`, then `docker compose up -d <service>`, in the project's working directory.
3. Verify.
4. Failure: `docker tag <old image ID> <image:tag>`, then `docker compose up -d --pull never <service>`.

### Rollback limitation

Rollback restores the container, not its data. When a new version migrates a database schema, the data stays migrated. Therefore:

- Breaking updates show an explicit warning before update and are never auto-updated.
- v0.2 adds an optional appdata snapshot before the update.

### Self-update and protected containers

- nextupdate never updates itself in-process. It starts a short-lived helper container that performs the update.
- The socket-proxy and database containers (detected by image name) default to the `notify` policy.

## Data model (SQLite)

Containers are keyed by name, because the container ID changes on every recreate.

- `containers`: name, source, compose project/service, image, tag, local digest, repo, policy, check config (health window, HTTP URL).
- `available`: container, remote digest, old version, new version, breaking flag, breaking reason, detected at.
- `history`: id, container, from/to digest, from/to version, started, finished, result (`ok`, `rolled_back`, `failed`), step log.
- `journal`: in-flight update steps for crash recovery.
- `changelog_cache`: repo, tag, body, published at, ETag.
- `mappings`: image → repo, source (`label`, `community`, `manual`).
- `users`, `push_subscriptions`, `notifiers`, `settings`.

## Error handling

- **Crash mid-update:** every step is written to `journal` before it runs. On startup, `reconcile` finishes or undoes half-done updates, including a leftover `<name>-nu-old`.
- **Concurrency:** one update per container at a time; a global queue runs updates sequentially by default.
- **Registry limits:** manifest HEAD requests do not count toward the Docker Hub pull limit. Back off on 429. Optional credentials per registry.
- **GitHub limits:** 60 requests/hour unauthenticated. Optional token; ETag cache. No changelog found → UI shows "no release notes" with a link to the registry page.
- **Unreachable registry or GitHub:** the check is marked stale; no update is offered on stale data.

## Security

- nextupdate has write access to the Docker socket, which is root-equivalent on the host.
- Own login (single admin user in v0.1), session cookies, CSRF protection on mutating endpoints.
- Documentation recommends docker-socket-proxy with only the needed endpoints enabled.
- The widget endpoint uses a separate read-only token.

## UI

Vanilla JS, same visual language as nextdash. English only in v0.1, i18n-ready.

- **Main list:** containers grouped as *breaking*, *update available*, *up to date*. Each row: icon, old → new version, source badge (compose/run), policy, update button.
- **Detail panel:** changelog of all releases between the two versions, keyword highlights, repo links.
- **History:** timeline of updates and rollbacks, expandable step log, "roll back to previous" button.
- **Settings:** check interval, notifiers, GitHub token, registry credentials, per-container checks and policy.
- **PWA:** manifest, service worker, push; light and dark themes.

The layout is chosen from mockups before any UI is built.

## nextdash integration

`GET /api/widget?token=…` returns:

```json
{ "updates": 4, "breaking": 1, "lastCheck": "2026-09-25T18:00:00Z", "url": "https://nextupdate.local" }
```

A nextdash custom widget polls it on its own TTL. Clicking opens nextupdate.

## Testing

- **Go unit tests:** classifier (semver, keywords), changelog range selection, policy decisions, journal and reconcile. Docker and registry behind interfaces with fakes.
- **Integration tests:** real Docker in CI with test images (`v1` healthy, `v2` unhealthy) that prove update and rollback for each adapter.
- **E2E:** Playwright on port 8099 with `PW_WORKERS=2`, core flows only.

## Open questions for implementation planning

- Exact list of database images for the default `notify` policy.
- Default verification window (proposal: 60 s, crashloop = 3 restarts).
- Where community mappings live before the v0.2 repository exists (proposal: a bundled JSON file).
