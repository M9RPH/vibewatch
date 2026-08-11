# Vibewatch V0.7.2.1 Architecture

## Control plane

```text
Browser
  │
  ▼
Vibewatch controller (Go + React + SQLite)
  ├─ RBAC / sessions
  ├─ policy automation
  ├─ queue / audit / history / logs
  ├─ Pushover routing
  ├─ OCI registry metadata
  ├─ worker lifecycle manager
  └─ self-update coordinator
        │
        ├──────── Docker host A (local/remote)
        │            ▲
        │            │
        │       Worker A (passive)
        │
        ├──────── Docker host B
        │            ▲
        │       Worker B (passive)
        │
        └──────── Docker host N
                     ▲
                Worker N (passive)
```

Workers run centrally on the controller's Docker daemon. Each worker has a stable name derived from `host_id`, a unique HTTP API token, and exactly one target `DOCKER_HOST`. Remote target hosts need no Watchtower agent installed.

## Worker lifecycle

Workers do not self-update and do not run autonomous update schedules. The controller owns their lifecycle. A worker image maintenance run pulls the configured Watchtower image and, only if the image ID changed, recreates workers sequentially from persisted host records.

The `workerOpMu` read/write lock prevents worker replacement while a check/update is actively using a worker and prevents new worker operations during recreation.

## Policy automation

Automation contains only **schedule + target scope**. Existing automation rows can be edited in place, paused without deleting their schedule/target, and resumed later. The policy on each container is authoritative:

- `manual`: check, cache result, optionally notify; no automatic install.
- `auto`: check then queue an install if Watchtower reports an update and that concrete digest is not snoozed.
- `ignore`: never pull/update the container; refresh only read-only local/registry digest information.

## Version metadata

Three independent data paths are intentionally kept separate:

1. **Manual/Auto image state:** Watchtower compares immutable image digests and remains authoritative for update availability.
2. **Excluded image state:** Vibewatch compares the local Docker image-config digest (`ImageID`) with the config digest referenced by the matching platform-specific public OCI/Docker registry manifest. This path never pulls image layers and never invokes a container update action. Platform selection uses the target image OS/architecture/variant, avoiding cross-architecture false positives on multi-platform tags.
3. **Readable metadata:** local image labels/tags plus public OCI/GitHub metadata provide installed/latest version strings and release notes. These values are informational and may legitimately be unknown.

GitHub release metadata is optional and primarily supplies patch notes/fallback readable release version. This prevents a release-note source from becoming an unsafe update decision source.

## RBAC

- **Owner**: root environment account, global scope + system lifecycle + Admin management.
- **Admin**: global operational scope + hosts/groups/automation + User management.
- **User**: effective host scope = direct host grants UNION hosts in granted groups.

Protected backend routes resolve role/host access server-side. Pushover routing uses the same effective host calculation.

## Pushover

Pushover credentials are fully per-account. Every Owner/Admin/User stores an independent Pushover Application API Token/Key plus an independent Pushover User Key in SQLite together with that account's notification preferences. Delivery always uses the target account's own token/key pair and is filtered through effective host permissions. Application tokens are backend-only fields and are never serialized to the browser or support bundle. Manual update-available notifications are deduplicated by per-user/host/container/event fingerprint.

## Controller self-update

Once deployed from a registry-backed mutable tag, the Owner can launch a short-lived local Watchtower helper targeting only the controller container. The controller never attempts to rewrite its own executable or Compose file.

## Persistence

All mutable controller state lives beneath `WTUI_DATA_DIR` (default `/data`). Compose maps this from `${WTUI_DATA_PATH:-./data}`, so replacing/recreating the application image does not replace its state. SQLite stores hosts, private worker tokens, policies, update/version cache, jobs/logs, automations, groups, users/assignments, notification preferences/state and system settings; `/data/logs` stores controller logs and `/data/backups` stores database snapshots.

At runtime the Owner settings page verifies that `/data` is actually backed by a writable Docker bind mount or named volume. Existing databases are snapshotted before startup migrations. Manual/scheduled controller self-updates also create a consistent SQLite `VACUUM INTO` backup before Watchtower is allowed to recreate the controller. Startup snapshots retain the newest five; explicit/pre-self-update snapshots retain the newest ten.

Secrets/configuration that intentionally stay outside SQLite (`.env`: Owner bootstrap password, session secret, optional GitHub token and published app image) remain on the Docker host and are preserved by Compose/Watchtower container recreation. Pushover App Tokens and User Keys are persisted per account in SQLite. Light/dark theme and sidebar collapsed state are browser-local preferences. Worker tokens/password hashes/Pushover credentials are intentionally omitted from support bundle exports.

## Frontend fault isolation

Route content is wrapped in a React error boundary. API payloads with optional assignment lists are normalized to empty arrays before entering component state. Browser render/unhandled errors are forwarded to `POST /api/client-errors`, written to the structured controller log, and therefore become visible in support bundles.


## Container stack discovery

Container relationships are inferred from Docker's own container labels, not from container-name guessing. Compose/Portainer Compose projects use `com.docker.compose.project` and `com.docker.compose.service`. Swarm stacks use `com.docker.stack.namespace` and `com.docker.swarm.service.name`. Containers without these labels are presented as standalone. Stack metadata is informational and does not change update behavior.

## Collapsible desktop navigation

The desktop sidebar has expanded (256 px) and compact (80 px) states. Compact mode keeps only navigation/status icons in the rail and renders text in hover tooltips. The selected state is stored in browser localStorage (`wtui-sidebar-collapsed`) and is independent from server persistence. Mobile navigation keeps the full brand/header behavior.


## System-managed containers

The Vibewatch controller and its dedicated Watchtower workers remain visible in inventory for topology/health context, but are tagged `system_managed` by the controller API. Normal bulk scans, policy automation, manual update endpoints and policy writes skip/reject these containers. Their lifecycle is controlled only through Owner maintenance settings.

V0.3.9 migrates Docker runtime objects to Vibewatch-native names: `vibewatch`, `vibewatch-worker-*`, `vibewatch-self-updater`, and `vibewatch-internal`. The Compose service is now `vibewatch`. A short-lived `vibewatch-runtime-migrate` service removes pre-rebrand Docker runtime objects before controller startup and does not mount `/data`. Legacy `WTUI_*` environment-variable names are retained temporarily so existing `.env` files continue to work. `/data/watchtower-ui.db` is atomically renamed to `/data/vibewatch.db` on first start.


## Host health and image storage

The Dashboard reads host metadata directly through each configured Docker endpoint. `docker info` supplies engine/capacity metadata, `docker system df` supplies daemon-accounted image disk usage and reclaimable space, and `docker stats --no-stream` supplies aggregate running-container CPU/memory usage. These container runtime metrics are intentionally not labeled as whole-host CPU/RAM utilization.

Image inventory is based on unique local image IDs. An image is considered **in use** when any existing running or stopped container references its image ID; otherwise it is **unused**. Dangling images are the subset of unused/local images without a repository tag. Admin/Owner cleanup computes unused images first and removes eligible image IDs individually. Image IDs referenced by retained pre-update recovery snapshots are marked rollback-protected and skipped, so a manual cleanup cannot invalidate a still-retained Vibewatch rollback point. Cleanup remains manual only and is persisted in jobs/audit/application logs.


## SSH Quick Setup

Owner-only Quick Setup is a one-time bootstrap path for Linux/systemd Docker Engine hosts. The controller executes `sshpass` + OpenSSH from inside Vibewatch, accepts the remote host key into `/data/ssh/known_hosts`, and never writes the supplied SSH/sudo password to disk. It creates a Vibewatch-owned systemd override that attempts to add `tcp://<host-ip>:2375`, restarts Docker, verifies local Docker health, and rolls back that override if the restart/health check fails. Vibewatch then verifies the TCP endpoint from the controller before persisting the host and launching its worker.

This is intentionally treated as a legacy/high-trust LAN feature. The endpoint is bound to the exact IPv4 address supplied by the Owner, requires a UI acknowledgement, and should be constrained by firewall/VPN policy.

## Pushover configuration layers

There is no shared active Pushover credential in V0.6.0. Each signed-in account stores its own Pushover Application API Token/Key, User Key and event preferences. Delivery targets are filtered by effective host permissions before messages are sent, then each target is contacted with that target account's own credential pair. During upgrade only, an old shared `pushover_app_token` setting or legacy `PUSHOVER_APP_TOKEN` environment value is imported once into the Owner account if the Owner has no personal App Token yet; it is never inherited by Admins or Users.


## Worker isolation (introduced V0.4.5)

Workers are physically hosted on the Vibewatch controller Docker daemon even when they operate a remote `DOCKER_HOST`. Remote worker containers therefore receive the controller-side label `com.centurylinklabs.watchtower.scope=vibewatch-worker-<host-id>`. No `WATCHTOWER_SCOPE` is passed to the worker process itself, so target-container selection remains unchanged. The local Docker-host worker stays unscoped. This prevents the local Watchtower instance's same-scope instance cleanup from removing the controller's remote-worker siblings while preserving normal checks/updates on every managed host.

## Pushover delivery auditing (introduced V0.4.5)

`notification_deliveries` stores non-secret metadata for each actual Pushover API send attempt: timestamp, recipient account, host/container context, event class, title, accepted/failed status and sanitized error text. App Tokens and User Keys are never stored in delivery rows or support-bundle exports. Owner/Admin can inspect global delivery history; a User can inspect only its own delivery history. Delivery history is capped at the most recent 5000 records.


## Container recovery snapshots

Vibewatch stores configuration-only recovery snapshots below `/data/backups/containers/`. A backup unit is either a detected Compose/Swarm stack or one standalone container/service on one Docker host. Each unit retains the Owner-configured number of newest snapshots (1–20, default 3). Lowering the setting removes oldest excess snapshots immediately.

A snapshot ZIP contains:

- `compose.yaml` reconstructed from Docker runtime state;
- `container-inspect.json` with the original low-level Docker inspect data;
- `images.json` with image metadata for the referenced images;
- `volumes.json` with referenced Docker-volume metadata only;
- `backup-info.json` describing host, unit, reason, timestamp and reconstruction limitations.

Volume contents are deliberately excluded. Runtime environment values are included for disaster recovery, so the files are created with restrictive filesystem permissions and download APIs are restricted to Admin/Owner roles.

Manual and automated update jobs create a snapshot before invoking Watchtower. Snapshot failure blocks the update so the recovery guarantee cannot silently be bypassed.

## Docker volume maintenance

Volume inventory is host-level administration and is restricted to Admin/Owner roles. Bulk cleanup invokes Docker's default anonymous-only volume prune. Named volumes are never bulk-pruned and can only be removed one at a time after Vibewatch verifies that no existing container references the volume. Bind mounts are outside Docker's volume lifecycle and are never deleted by Vibewatch.


## Docker event storage (V0.5.2)

Docker events are diagnostic, write-heavy data and are deliberately isolated from `vibewatch.db`. The event watcher appends meaningful records to `/data/logs/docker-events.jsonl`, filters `exec_create`, `exec_start` and `exec_die`, and retains at most 5,000 records. On upgrade, Vibewatch can recover the known V0.5.0/V0.5.1 event-only SQLite corruption by verifying every core table before rebuilding a clean primary database; broader corruption is never automatically modified.


## Update history and rollback (V0.6.0)

Update execution records a durable per-container history row after the job finishes. A successful update references the recovery snapshot created immediately before Watchtower was invoked. While that snapshot still exists, authorized users may request a manual rollback for Compose/standalone containers. Vibewatch creates another safety snapshot before rollback, restores the previous immutable/local image and saved runtime configuration, verifies the container can be queried again, then records the rollback itself as another history row. One-click Swarm rollback and automatic health-based rollback are intentionally not performed in V0.6.0.

## Configuration drift (V0.6.1)

Vibewatch keeps recovery snapshots and drift baselines separate. Recovery snapshots remain immutable rollback artifacts, while Config Drift stores a secret-safe normalized baseline in SQLite. Snapshot creation seeds that baseline, and successful updates/rollbacks refresh it from the resulting live container configuration so expected image changes do not become false drift. Legacy V0.6.0 rows whose newest baseline predates a successful image operation are rebaselined once. Environment and user-label values are hashed; drift details expose changed keys only. The current Docker inspect configuration is compared with this baseline periodically, while volatile runtime fields and the immutable image ID remain excluded.

## Update snooze and first-detected tracking (V0.7.0)

`container_cache` persists the first time the currently remote image digest was observed plus an optional snoozed digest/timestamp. Snooze is deliberately bound to one immutable remote digest instead of changing the container policy: while that exact digest remains latest, Vibewatch suppresses update availability for installation/automation/notification purposes. A different latest digest is treated as a new update, clears the old snooze automatically and receives a new first-detected timestamp. Unsnooze restores availability immediately when the current and latest digests differ.

## Docker network and build-cache maintenance (V0.7.0)

Network inventory is queried separately for each configured Docker host. Only unused custom networks are cleanup candidates; Docker default networks, ingress/worker networks, Swarm-scoped networks and every network currently attached to a container are excluded. Custom networks referenced by retained pre-update snapshots are additionally rollback-protected. Network removal is performed one network at a time so one failure does not turn into a blanket prune.

Build-cache usage is read from Docker's own `system df` accounting. Admin/Owner cleanup invokes Docker builder pruning only after an explicit UI action and reports before/after/reclaimed values in the job/audit path.

## Private registry credentials (V0.6.0)

Owner-only registry credentials are stored in SQLite as AES-256-GCM ciphertext. The 256-bit encryption key is generated locally at `/data/registry-credentials.key`, mode `0600`, and is never exposed through normal APIs, logs or support bundles. Credentials are supplied only to Vibewatch registry metadata requests; Watchtower/Docker pull authentication remains a target-daemon concern. The full `/data` directory therefore becomes the restoration unit once private registry credentials are configured.
## Volume retention protection and inventory verification (V0.7.1)

Retained pre-update snapshots now protect named or anonymous Docker volumes referenced by their `volumes.json` or captured container mounts. Volume inventory is host-scoped from `docker volume ls`; the normal reference path inspects all local containers in batches, while any incomplete container scan triggers a bounded per-volume `docker ps --filter volume=...` verification. The API exposes concrete referencing container names, rollback-protection state and retained-snapshot count. Cleanup never uses blanket `docker volume prune`, because that operation cannot exclude retained rollback volumes.

The Dashboard keeps inventory and cleanup actions separate. Inventory counts are loaded per Docker host for Images, Volumes and Networks; fetch failures remain explicit unavailable states rather than zero-value inventories. Cleanup tiles show only eligible objects after rollback/system/in-use protection, plus Docker-reported reclaimable Build Cache.

