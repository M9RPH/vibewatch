# Vibewatch V0.7.2.1

A central multi-host Docker update controller using the Nicholas Fedor Watchtower fork as an intentionally passive update worker.

## V0.7.2.1 highlights

- **Dashboard inventory tile polish:** the Images, Volumes and Networks `Open` buttons now sit directly below their counters, preventing overlap on narrow host cards while leaving the cleanup layout and behavior unchanged.

## V0.7.1 highlights

- **Retention-aware volumes:** Volumes referenced by retained pre-update snapshots are marked as rollback protected (with retained-snapshot count) and excluded from both named and anonymous cleanup.
- **Volume inventory verification:** Per-host volume references now expose concrete container names. If normal container inspection is incomplete, Vibewatch falls back to Docker's volume filter per volume instead of treating the whole host as one ambiguous result. Inventory fetch failures are shown as unavailable instead of silently appearing as zero volumes.
- **Dashboard host actions:** Per-host Docker actions are grouped into consistent Inventory and Cleanup panels with matching counts, protection context and action buttons for Images, Volumes, Networks and Build Cache.

## V0.7.0 highlights

- **Update snooze + first detected:** each concrete pending image digest records when Vibewatch first detected it. A container can snooze exactly that digest; manual/automatic installation and new-update notifications skip it until a different remote digest appears, at which point the snooze expires automatically.
- **Rollback-safe image cleanup:** retained pre-update recovery snapshots protect the old image IDs they reference. Unused-image cleanup now removes eligible images individually instead of using a blanket `docker image prune -a`, so retained rollback points are not invalidated by Vibewatch cleanup.
- **Editable/pausable automations:** Admin/Owner can edit an existing policy run in place, pause it without deleting the schedule, and resume it later.
- **Owner-configurable recovery retention:** Settings controls 1–20 recovery snapshots per stack/standalone service (default 3). Lowering retention removes oldest excess snapshots immediately; rollback protection follows only retained snapshots.
- **Docker Networks maintenance:** Admin/Owner can inspect networks per host and remove unused custom networks. Docker default/ingress/worker and Swarm-scoped networks, networks in use and networks referenced by retained rollback snapshots are protected.
- **Docker Build Cache maintenance:** Dashboard host details expose total/reclaimable Build Cache and provide an explicit Admin/Owner cleanup action for Docker-reported reclaimable builder cache.

## V0.6.1 highlights

- **Config Drift baseline fix:** normal Vibewatch updates no longer create drift merely because the recovery snapshot predates the new image. Recovery snapshots remain rollback artifacts; drift uses its own secret-safe baseline and refreshes it after successful update/rollback operations.
- **Volume inventory hardening:** Docker volumes are enumerated per host, inspected in bounded batches with individual fallback, and incomplete reference scans are shown as unknown rather than falsely unused.
- **Dashboard/UI cleanup:** rounded Needs-attention tiles, Image/Volume dialogs widened to 1070 px, redundant Available updates/Policy model panels removed, and a larger responsive sidebar logo with Vibewatch underneath.

## V0.6.0 highlights

- **Update History:** successful/failed manual and automatic updates are stored per container with versions, image IDs/digests, trigger, actor, duration and the recovery snapshot used before the update. The History page provides a global Update History tab and every container has a direct History action.
- **Manual rollback:** a successful Compose/standalone update can be rolled back while its pre-update recovery snapshot is still retained. Vibewatch first creates a new safety snapshot, then restores the previous immutable/local image and saved runtime configuration. Rollback is manual only; no health-based automatic rollback is performed. One-click Swarm rollback remains disabled in this release.
- **Config Drift Detection:** current Docker runtime configuration is compared with the latest recovery snapshot. Image reference, environment, command/entrypoint, ports, mounts, networks, restart policy, healthcheck, privileges/capabilities, DNS/devices and user labels are compared while volatile runtime fields and the immutable image ID are ignored.
- **Private registry credentials:** Owner-only encrypted credentials can be stored for Docker Hub, GHCR or custom OCI registries so read-only manifest/digest/version metadata checks also work for private images. Secrets are AES-256-GCM encrypted and never returned by the API/support bundle.
- **Container Backups – Download all:** Admin/Owner can download the currently retained recovery snapshots across permitted hosts as one archive. The existing limit of three snapshots per stack/standalone service remains unchanged.
- **Dashboard attention summary:** the existing Dashboard now surfaces updates, unhealthy/restarting containers, offline hosts/workers, config drift, reclaimable images and unused anonymous volumes without introducing a second dashboard.
- Docker Events remain isolated in bounded `/data/logs/docker-events.jsonl`, and the V0.5.2 event-corruption recovery remains active.

## Policy semantics

| Policy | Scheduled policy run | Manual check | Manual update |
|---|---|---|---|
| Manual | Watchtower digest check; never install | Yes | Yes |
| Auto Update | Watchtower digest check; queue install when changed | Yes | Yes |
| Excluded | Read-only local/registry digest info only | Direct action blocked; Check All may refresh read-only info | Blocked until policy changes |

The Watchtower workers themselves remain passive. V0.7.2.1 continues to start them with `WATCHTOWER_HTTP_API_PERIODIC_POLLS=false` and `WATCHTOWER_UPDATE_ON_START=false`. Updates are initiated only by Vibewatch manual actions, policy automation, worker maintenance, or the explicit Owner-only application self-update helper.

## Worker maintenance

Worker auto-maintenance is enabled by default and runs at `03:30` daily (`30 3 * * *`, controller `TZ`). The Owner can change or disable it in **Settings → Worker maintenance**.

The controller:
1. pulls `WTUI_WATCHTOWER_IMAGE` on its local Docker daemon;
2. compares the local image ID before/after pull;
3. if unchanged, does nothing;
4. if changed, waits for active worker operations to finish;
5. recreates enabled host workers sequentially from the stored Host ID, Docker endpoint and private worker API token;
6. waits for each worker to become ready and logs/audits failures.

The worker never decides to upgrade itself.

## Version sources

For each container Vibewatch keeps update availability and readable version metadata separate:

- **Manual/Auto update state:** Watchtower digest comparison (authoritative).
- **Excluded image state:** read-only comparison of the local Docker image-config digest with the matching platform-specific registry config digest; no pull/update action. Multi-platform tags are resolved for the target host architecture.
- **Installed version:** local image labels such as `org.opencontainers.image.version`, or a pinned non-floating image tag.
- **Latest readable version:** public Docker Hub/GHCR/OCI manifest/config labels when available.
- **Patch notes:** GitHub release source, automatically detected where possible or configured in the policy.

Public Docker Hub and GHCR images work without an extra registry credential. Private registry metadata authentication can be configured by the Owner under Settings. These credentials are used only by Vibewatch read-only registry metadata checks; Watchtower/Docker update credentials remain managed by the target Docker daemon.

## Pushover

Every Vibewatch account configures both of its own Pushover.net credentials under **Settings**:

- **Application API Token/Key** – from that user's own Pushover application;
- **User Key** – from that user's Pushover account.

Each account independently enables:
- automatic-update success/failure notifications;
- newly detected updates on Manual-policy containers;
- manually initiated update success/failure notifications.

Owner/Admin/User credentials are never shared. Recipients are resolved server-side by effective host permissions, so scoped Users only receive events from assigned hosts/groups. Only "new Manual update available" alerts use digest fingerprinting to avoid duplicate alerts for the same image; manual update-result alerts are emitted for each actual manual update attempt/result.

For upgrade compatibility, `PUSHOVER_APP_TOKEN` may remain in an old `.env`; V0.6.0 treats it only as a one-time Owner migration source when the Owner has no personal App Token yet. New installations should configure Pushover exclusively in the per-account Settings UI.

## Roles

### Owner
The environment-backed root account (`admin` login) is the single Owner. It can do everything, including:
- manage Admin and User accounts;
- manage hosts, groups and automations;
- manage worker maintenance;
- launch/configure application self-update;
- access global diagnostics and support bundles.

### Admin
- global operational access to all hosts;
- manage hosts, host groups and automations;
- manage normal User accounts;
- run checks/updates and change container policies;
- no Owner-only worker/app maintenance and no Admin-account management.

### User
- sees only directly assigned hosts and hosts inherited through assigned groups;
- can inspect containers, check updates, run permitted updates, change policies, view applicable history/events and configure personal Pushover;
- cannot manage hosts/groups/automations/users/system maintenance.

Authorization is enforced by the backend, not only by hiding UI controls.

## Host groups

Groups can be reused for both automation and permission assignment. Adding a new host to an already-assigned group automatically extends the User's effective host scope.

## Persistence / what survives updates

The application image is treated as disposable. Mutable state is kept outside it:

| Data | Location | Survives container/image recreation |
|---|---|---|
| Hosts + worker tokens | SQLite under `/data` | Yes |
| Users/roles/permissions/groups | SQLite under `/data` | Yes |
| Container policies + custom release sources | SQLite under `/data` | Yes |
| Automations + system maintenance settings | SQLite under `/data` | Yes |
| Per-user Pushover App Token + User Key + preferences | SQLite under `/data` | Yes |
| Update/version cache, jobs, audit history + update/rollback history | SQLite under `/data` | Yes |
| Config-drift cache + encrypted registry credentials | SQLite under `/data` + `/data/registry-credentials.key` | Yes, when the full `/data` directory is retained |
| Controller logs | `/data/logs` | Yes |
| Database backups | `/data/backups` | Yes |
| Owner password | bootstrap in host-side `.env`; optional persistent override hash under `/data` after UI password change | Yes |
| Session secret, optional GitHub token, app image | host-side `.env` | Yes, if `.env` is retained |
| Theme + collapsed-sidebar preference | browser localStorage | Yes in that browser |
| Web UI/code/logo | application image | Replaced by the new version by design |

Compose maps `${WTUI_DATA_PATH:-./data}` to `/data`. The default therefore remains backward-compatible with existing installations. For a future GitHub/registry installation, a stable absolute host path is recommended, for example `WTUI_DATA_PATH=/opt/vibewatch-data`; do **not** change an existing installation to an empty new path without first moving/copying its current `data` directory.

The Owner Settings page inspects the running controller via Docker and reports whether `/data` is actually backed by a writable bind mount/named volume. Before startup schema migrations an existing database receives a `pre-start-*` snapshot. App self-update creates another consistent backup before replacement.

## Application self-update

Self-update is intentionally disabled while using a locally built image. After publishing the project, use the same mutable registry tag in Compose and `WTUI_APP_IMAGE`, for example:

```env
WTUI_APP_IMAGE=ghcr.io/YOURNAME/vibewatch:latest
```

The Owner can then check/trigger self-update under Settings. Before the helper is launched, the controller creates a consistent SQLite backup in the persistent `/data/backups` directory. It then launches a short-lived Watchtower helper on the **local controller Docker socket** and targets only the `vibewatch` container. This lets the helper preserve the running container configuration, environment and mounts while replacing it with a newer image.

Scheduled app self-update is off by default; default prepared schedule is Sunday 04:15.

## Branding / logo

The supplied whale/watchtower logo is bundled at:

```text
web/public/logo.png
```

It is used by the login/sidebar branding and as the browser favicon/apple-touch icon. If the file cannot be loaded, the UI falls back to the built-in `W` tile.

## UI robustness and browser diagnostics

Empty host groups and empty User assignment lists are valid states and are serialized as explicit arrays. The frontend also normalizes older payloads that omitted these arrays. A React page error boundary prevents a single page render failure from blanking the whole application; browser render and unhandled errors are forwarded to the controller application log for inclusion in support bundles.

## Permission editing

Existing User accounts can be opened with **Edit permissions**. Host groups and direct host grants can be added or removed at any time; the editor shows the resulting effective host access before saving. Admin remains a global role and therefore does not use host-scoped grants.

## Installation

```bash
cp .env.example .env
# edit .env
mkdir -p data
# Optional: set WTUI_DATA_PATH=/opt/vibewatch-data for a stable external host path
docker compose up -d --build
```

Open `http://SERVER:8085` (or `WTUI_PORT`). Login name for the Owner is `admin`. Initially the password is `WTUI_ADMIN_PASSWORD`; after changing it under Users → Managed accounts, the persistent Vibewatch password takes precedence.

### Upgrade to V0.7.2.1

Copy the existing **entire `data/` directory** and `.env` into the V0.7.2.1 project directory, then run:

```bash
docker compose up -d --build
```

V0.7.2.1 remains backward-compatible with the V0.7.2 data directory and requires no new SQLite migration. This release is UI-only and adjusts the Dashboard host inventory tile layout; runtime Docker inventory/cleanup semantics and persisted state remain unchanged. Existing hosts, users, groups, policies, automations, Pushover settings, recovery snapshots, Config Drift baselines and logs remain compatible. Once private registry credentials are configured, `/data/registry-credentials.key` is required together with `vibewatch.db`; do not migrate only the database file. Database backups created by Vibewatch also preserve a companion registry-key copy when one exists.

## Publishing later

The included GitHub Actions workflow builds `linux/amd64` and `linux/arm64` images and publishes to GHCR on `main`/`v*` pushes. The default branch also receives `latest`, making it suitable as the future `WTUI_APP_IMAGE` self-update channel.

## Current limitations

- GitHub patch notes require a detectable/configured GitHub repository; non-GitHub projects can still use registry version information and Watchtower update detection.
- Manual rollback is available for Compose/standalone containers only while the linked pre-update recovery snapshot is still retained and the previous image can be resolved locally or by an immutable repository digest. Swarm entries remain in history/backups but do not expose one-click rollback. Rollback restores container configuration, not application data inside volumes/databases.
- Automatic rollback based on Docker health status is intentionally not enabled because many containers do not define a healthcheck.
- The application self-update path only becomes meaningful once the controller itself is deployed from a registry image rather than a locally built tag.
