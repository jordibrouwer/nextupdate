# nextupdate

Update cockpit for Docker containers. It shows what changes before you update (release notes between your version and the new one), flags breaking updates, updates with one click or by per-container policy, and rolls back automatically when the new container fails its health check.

Works with Docker Compose services and plain `docker run` containers on one host.

## Run it

```yaml
services:
  nextupdate:
    image: nextupdate:dev          # build with: docker build -t nextupdate:dev .
    restart: unless-stopped
    ports:
      - "8099:8099"
    environment:
      NEXTUPDATE_BASE_URL: "http://your-server:8099"
      # NEXTUPDATE_GITHUB_TOKEN: "..."   # optional, raises the GitHub rate limit
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
      # Compose projects: mount each project directory at the SAME path as on the host.
      - /srv/stacks:/srv/stacks
```

The first visit to the API creates the admin account (`POST /api/setup`); the web UI for this comes in a later release.

## Security

nextupdate can create and remove containers, so anyone who can sign in controls the Docker host. Put it behind HTTPS (a reverse proxy) and use a long password. For a smaller blast radius run a `docker-socket-proxy` and set `DOCKER_HOST=tcp://socket-proxy:2375`.

The `/data` folder holds the database with notifier secrets (ntfy, Gotify, Telegram tokens, SMTP password) in plain text. Protect it like the Docker socket.

## Command line

| Command | What it does |
|---|---|
| `nextupdate serve` | checks on a schedule, applies policies, serves the API on `:8099` |
| `nextupdate check` | lists containers with a newer image |
| `nextupdate update <name>` | updates one container, rolls back on failure |
| `nextupdate policy <name> <notify\|auto\|never>` | sets the update policy |
| `nextupdate reconcile` | repairs an update that a crash interrupted |

Policies: `notify` (default) tells you; `auto` updates patch and minor versions but never breaking updates, databases or unknown versions; `never` stays silent.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker endpoint |
| `NEXTUPDATE_DATA` | `/data` | data directory |
| `NEXTUPDATE_LISTEN` | `:8099` | HTTP listen address |
| `NEXTUPDATE_BASE_URL` | empty | public URL, used in notification links and the widget |
| `NEXTUPDATE_INTERVAL` | `6h` | time between checks |
| `NEXTUPDATE_VERIFY_WINDOW` | `60s` | how long a new container gets to become healthy |
| `NEXTUPDATE_KEEP_OLD_IMAGES` | `168h` | how long the previous image is kept for a rollback |
| `NEXTUPDATE_GITHUB_TOKEN` | empty | optional GitHub token |
| `NEXTUPDATE_PUSH_SUBJECT` | `mailto:nextupdate@localhost` | contact address sent to push services |

## nextdash widget

Get the token from `GET /api/widget/token` (signed in). Then point a nextdash custom widget at `http://your-server:8099/api/widget` with the header `Authorization: Bearer <token>`. It returns `{"updates": N, "breaking": M, "lastCheck": "...", "url": "..."}`.
