# Vibewatch V0.8.2.1 Architecture

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

Vibewatch stores runtime/configuration recovery snapshots below `/data/backups/containers/`. A backup unit is either a detected Compose/Swarm stack or one standalone container/service on one Docker host. Each unit retains the Owner-configured number of newest snapshots (1–20, default 3). Lowering the setting removes oldest excess snapshots immediately and expires any V0.8.0 full restore points linked to the removed snapshot.

A snapshot ZIP contains:

- `compose.yaml` reconstructed from Docker runtime state;
- `container-inspect.json` with the original low-level Docker inspect data;
- `images.json` with image metadata for the referenced images;
- `volumes.json` with referenced Docker-volume metadata only;
- `backup-info.json` describing host, unit, reason, timestamp and reconstruction limitations.

Volume contents are deliberately excluded. Runtime environment values are included for disaster recovery, so the files are created with restrictive filesystem permissions and download APIs are restricted to Admin/Owner roles.

Manual and automated update jobs create a snapshot before invoking Watchtower. For non-Swarm update targets V0.8.0 then commits the target container with `docker commit --pause=true` into an internal `vibewatch-restore/...` image and records a `restore_points` row that links the writable-layer image, original runtime/image metadata, attempted target digest and snapshot. Snapshot or writable-layer capture failure blocks a non-Swarm update so the full-container recovery guarantee cannot silently be bypassed. Swarm targets remain configuration-only and therefore do not create a one-click full restore image.

## Docker volume maintenance

Volume inventory is host-level administration and is restricted to Admin/Owner roles. Vibewatch does not use a blanket volume prune: anonymous/named cleanup candidates are verified individually so retained rollback volumes, unknown-reference volumes and in-use volumes can be excluded. Retained snapshot references protect a volume from Vibewatch deletion. Bind mounts are outside Docker's volume lifecycle and are never deleted by Vibewatch.


## Docker event storage (V0.5.2)

Docker events are diagnostic, write-heavy data and are deliberately isolated from `vibewatch.db`. The event watcher appends meaningful records to `/data/logs/docker-events.jsonl`, filters `exec_create`, `exec_start` and `exec_die`, and retains at most 5,000 records. On upgrade, Vibewatch can recover the known V0.5.0/V0.5.1 event-only SQLite corruption by verifying every core table before rebuilding a clean primary database; broader corruption is never automatically modified.


## Recovery Compose namespace normalization (V0.8.2)

Recovery snapshots are generated while all containers in the host inventory are still available. For any captured `HostConfig.NetworkMode=container:<ref>`, the snapshot writer resolves `<ref>` against the host inventory before serializing `compose.yaml`. If source and target are services from the same Compose project and the target service is part of the reconstructed unit, the durable form is `network_mode: service:<target-service>`. Otherwise the durable form is `network_mode: container:<target-container-name>`. A hexadecimal runtime ID that cannot be resolved is rejected and aborts snapshot creation; ephemeral IDs are never intentionally persisted as recovery configuration.

Namespace-sharing services suppress hostname/domainname, published ports, DNS and extra-host fields in the reconstructed Compose because those options conflict with Docker container-network mode. Independently, a hostname that is simply the container's Docker-generated own ID prefix is treated as runtime metadata and omitted, while an explicit non-runtime hostname is retained. Snapshot metadata schema version 2 identifies artifacts created with these normalization rules. Internal low-level rollback still restores from retained inspect data and explicitly rebinds dependents to the restored parent ID; the normalized Compose file is the portable/manual recovery representation.

## Network namespace dependency transactions (V0.8.1)

Before a manual or automated update becomes destructive, the controller inspects every container on that Docker host and builds the direct `NETWORK_NAMESPACE` dependency set for the target. A dependency exists when a dependent container's Engine-level `HostConfig.NetworkMode` resolves to `container:<target-id>`. Compose `network_mode: service:<service>` is therefore handled through Docker's actual runtime relationship rather than application-specific names or Compose-file parsing. The scan is fail-closed: Vibewatch does not recreate a parent when it cannot establish a complete host-level dependency view.

The update transaction records source/target IDs, source name, prior running state, original network mode and Compose project/service metadata. The normal parent recovery snapshot is reused when it already contains a dependent; a dependent from another backup unit receives a transaction snapshot. Cross-unit dependency snapshots are pinned while the parent Restore Point is active, so independent retention cannot invalidate only one leg of a rollback transaction. When the parent Restore Point expires, transaction-only dependency snapshots are removed with it.

After Watchtower returns successfully, Vibewatch verifies the parent using the existing running/health logic. If the parent ID changed, each dependent is removed and recreated from its captured runtime configuration with `--network container:<new-parent-id>`. Docker options incompatible with container network mode (hostname/domain, published ports and DNS/extra-host flags) are omitted during reconstruction. A dependent that was stopped before the update remains stopped; a previously running dependent is started and verified. These operations emit dependency audit/job events and are not recorded as independent image updates.

If dependent recreation fails, the parent update transaction is failed and the existing automatic full-restore path is eligible. Full rollback uses the inverse lifecycle: stop all known namespace dependents, restore and verify the parent, then recreate the retained dependent configurations against the restored parent ID. The rollback path also rescans the live parent before destructive work so a namespace dependent added after the Restore Point was created is preserved/rebound rather than accidentally orphaned. Config Drift baselines are refreshed after successful dependent recreation.

The first dependency implementation is intentionally limited to direct network-namespace lifecycle coupling. It does not infer arbitrary `depends_on`, database/application ordering, filesystem semantics or other service dependencies. Docker volume and bind content semantics are unchanged.

## Update history and full restore points (V0.8.0)

Every update still records durable per-container history, now with an optional `restore_point_id`. For a normal Compose/standalone update, the pre-update recovery snapshot is paired with a committed container writable-layer image. The internal restore image is protected from Vibewatch image cleanup while its restore point remains retained. Retention expiry marks the linked restore point expired and untags the internal restore image. Pre-V0.8.0 history remains compatible with the legacy image/config rollback path.

A full rollback recreates the target from the captured low-level runtime configuration and committed writable layer. The committed image is retagged to the original image reference when safe so future Watchtower checks continue following the original registry/tag instead of an internal Vibewatch tag. Before a destructive manual/full rollback Vibewatch creates a temporary committed safety image of the current container and uses it for best-effort recovery if restoration itself fails. The temporary image is removed after the attempt and is not counted against retained restore-point slots.

Automatic rollback is deliberately conservative. After an update, Vibewatch rolls back only when Docker provides a concrete failure signal: the container is missing/stopped/restarting, or an explicit Docker healthcheck reports `unhealthy`. A container without a healthcheck must remain running for a short stability window, but Vibewatch does not infer application-level correctness. A successful rollback refreshes Config Drift baseline state and snoozes the currently available remote update digest so an Auto Update policy cannot immediately reinstall the failed version.

Docker volumes and bind mounts are not included by `docker commit`. Their references are retained/protected where applicable, but their contents are not rolled back. Application/database migrations that modify persistent data therefore require a separate data-aware backup. Docker Swarm remains configuration-only for one-click recovery.

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

