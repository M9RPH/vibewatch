# Vibewatch V0.9.1

A central multi-host Docker update controller using the Nicholas Fedor Watchtower fork as an intentionally passive update worker.

## V0.9.1 cleanup reliability

V0.9.1 keeps the V0.9 transactional update/recovery architecture and hardens host-wide Docker cleanup, especially across remote/VPN Docker endpoints.

- **Visible cleanup progress:** Images, anonymous Volumes, Networks and Build Cache now run as persistent Jobs and display stage/percentage progress directly inside their Dashboard cleanup tile.
- **Request-independent cleanup:** the browser starts a cleanup and then polls Job status; reverse-proxy/browser timeouts can no longer cancel the Docker operation. This specifically fixes the observed NAS2 cleanup cancellation after about 90 seconds.
- **Remote cleanup budgets:** TCP/VPN hosts receive longer bounded inventory and per-object cleanup windows while LAN/local hosts keep shorter limits. A failed individual image/volume/network removal is isolated and the remaining eligible objects continue.
- **Reload-safe UX:** queued/running cleanup Jobs are rediscovered from the existing Jobs store after a page refresh and progress continues to be shown.
- **Short-screen Sidebar:** the controller/worker/user status panel no longer overlays navigation items; the menu becomes independently scrollable when vertical space is limited.
- **No DB migration:** V0.9.1 uses the existing Job/Job Log/lease schema from V0.9.0.

### Upgrade to V0.9.1

Copy the existing complete **`data/` directory** and `.env` into the V0.9.1 project directory and rebuild normally. No manual reconfiguration or schema migration is required.

## V0.9.0 reliability release

V0.9.0 deliberately focuses on making the existing Update/Preflight/Verification/Restore/Chain engine more transactional and recoverable rather than adding another updater path.

- **Durable Update Transactions:** every actual update receives a persistent transaction and state-machine stage linked to its Job, Config Snapshot, Restore Point and target digest. Critical transaction persistence must succeed before Docker mutation begins.
- **Crash recovery:** after a controller restart Vibewatch reconciles interrupted post-mutation transactions against the real Docker runtime. A healthy updated runtime is preserved; an unhealthy runtime is restored through the existing full Restore Point rollback path. Pre-mutation interruptions are safely aborted.
- **Hierarchical operation leases:** container mutations and host-wide cleanup cannot race. Unrelated containers remain independently updateable, while cleanup on one host is blocked whenever that host has an active update/rollback/chain lifecycle mutation.
- **Restore Point integrity:** retained snapshots, restore images, dependency snapshots, volumes and networks are validated before rollback and by periodic Recovery GC. Degraded points are visibly blocked instead of failing halfway through a restore.
- **Recovery GC:** scheduled every six hours and runnable manually from Container Rollback. It reuses existing retention/protection rules and removes only orphaned Vibewatch-owned restore images.
- **Richer Preflight diagnostics:** each check exposes its source, approximate duration and whether it is blocking. Transaction-persistence failures are Red/Blocked.
- **Verification history:** the latest 5,000 verification runs retain trigger, transaction, duration, result/error and detailed per-check timing; the Containers verification dialog shows recent history.
- **Real Docker regression lab:** `make test-integration` covers health warm-up plus the Gluetun-style stale namespace-ID lifecycle; `sudo make test-netem` optionally validates the fixture across isolated ~50 ms RTT namespaces with configurable loss.

### Upgrade to V0.9.0

Copy the existing complete **`data/` directory** and `.env` into the V0.9.0 project directory and rebuild normally. Startup creates a normal pre-migration SQLite backup before the additive migration. V0.9.0 adds reliability tables for transactions/leases/verification history/Recovery GC and three Restore Point integrity fields; existing V0.8.5 hosts, users, groups, policies, automations, chains, snapshots, Restore Points and history remain in place. No manual reconfiguration is required.

## V0.8.5 highlights

- **Expandable/grouped Dashboard hosts:** host cards start compact, expand to the existing full details, and are grouped by exact Host Group membership without combining host metrics.
- **Safer Docker Health warm-up:** temporary `unhealthy` states are given a bounded recovery grace window before the existing rollback engine is invoked; hard runtime failures still fail immediately.
- **Archived Config cleanup:** Admin/Owner can delete `Archived only` Container Configs; linked rollback protection expires consistently and pinned dependency snapshots are protected.
- **Light Mode consistency:** Update Chain stack-member cards and controls use the common Vibewatch surface/input design tokens in both themes.
- **Hosts layout:** Host Groups now live under configured Docker hosts in the main workspace.
- **No DB migration:** V0.8.5 is compatible with the existing V0.8.4.2 SQLite schema.

## V0.8.4.2 highlights

- **Stack-first Update Chains:** an Update Chain can now use a detected Docker Compose stack as its membership source. Selecting a stack imports every currently detected non-system container from that stack; Vibewatch still does **not** infer application dependency order. The user explicitly orders the imported services and can configure per-step waits.
- **Stack membership safety:** `Sync stack` refreshes a Stack Chain after services are added or removed. Vibewatch validates the live stack membership again before every run and blocks the chain if its saved members no longer match, preventing silent partial-stack updates.
- **One policy per managed stack:** Stack Chains own `Manual`, `Auto Update` or `Excluded` for the whole stack. Member rows on Containers show the chain-owned policy instead of editable per-container policy controls, eliminating conflicting policy sources.
- **Ordered-update enforcement:** a direct individual update of a Stack Chain member is blocked so the configured sequence cannot be bypassed. Per-container Check (except Excluded), Verification, release source, Snooze, History and Rollback remain available.
- **Automation reuse:** Auto stack chains execute in order through their bound existing Automation/Policy Run. Manual and Excluded stack chains can reuse that same schedule for update-information refresh/read-only checks without being independently updated. Normal policy scans never fall back to unordered updates for Auto stack members.
- **Backward compatibility:** existing V0.8.4/V0.8.4.1 free-form chains migrate as `Custom Chain` with inherited legacy per-container policy behavior. V0.8.4.2 additionally gives every chain step a backwards-compatible `current_action` default of `skip`; existing chain order, history, policies and automations are retained.
- **Current-member actions:** a step may `Skip`, `Restart` or safely `Recreate` an already-current member, but only when another member in the same run has an actionable update. If the whole chain is current (or only snoozed updates exist), the chain is a silent no-op with no lifecycle changes and no completion Pushover.

## V0.8.4.1 highlights

- **Dedicated Jobs page:** low-level Job History is no longer mixed into Update History. History now stays focused on update/rollback transactions, Preflight and Verification; Jobs provides the execution trail for checks, updates, rollbacks, chains and maintenance work.
- **Safe queued-job cancellation:** queued jobs can be cancelled before execution from the Jobs page. The transition is atomic in SQLite and queue workers atomically claim work, so a cancelled request cannot later be resurrected. Running Docker mutations remain intentionally non-interruptible.
- **Cancel queued container operations in place:** queued manual checks, updates and rollbacks expose a Cancel action directly in the per-container progress panel, including after a page refresh while the job is still queued.
- **Granular Preflight progress:** the update progress bar now exposes host/dependency inspection, health/verification configuration, registry/architecture checks, storage checks, mounts/recovery checks, Config Snapshot creation, Restore Point creation and the final Preflight decision before entering the existing update/health/verification stages.
- **No schema migration:** V0.8.4.1 reuses the V0.8.4 database schema and is backward-compatible with existing V0.8.4 data.

## V0.8.4 highlights

- **Shared safety pipeline:** manual updates, Auto Update policies and Update Chains all flow through the same `Preflight → Snapshot/Restore Point → Update → Docker Health → Custom Verification → Success/Rollback` engine. Chains call the normal per-container update job instead of implementing a second updater.
- **Update Preflight:** every update performs a single reusable readiness evaluation covering registry/manifest availability, target architecture, container state, recovery-artifact readiness, Docker network-namespace dependencies, Docker Healthcheck/custom verification coverage, named volumes, bind-mount observability, disk-space visibility and major-version metadata. Red checks block manual and automatic execution; yellow checks remain warnings.
- **Manual Preflight preview:** the Containers page previews the same preflight engine before a manual update. Preview is side-effect free; the execution pass repeats the safety checks and creates the actual Config Snapshot and Restore Point immediately before mutation.
- **Custom Verification:** optional container- or Compose-stack verification profiles support HTTP, HTTPS and TCP checks, expected HTTP status/content, start delay, retries and retry intervals. Docker Health remains the first post-start gate; failed custom verification enters the existing automatic rollback path and is persisted in Update History/Audit.
- **Post-rollback verification:** full Restore Point rollback now also runs the applicable custom verification profile after Docker Health. A rollback that restores the container but fails functional verification is reported as degraded/failed rather than as a false success.
- **Update Chains / Service Groups:** Admins/Owners can define explicit per-host ordered groups with per-step waits, stop-on-failure, optional rollback of previously completed members and optional binding to an existing Automation policy run. From V0.8.4.2, a detected Docker Compose stack can own the chain membership and a single stack-level Manual/Auto Update/Excluded policy; existing custom chains retain their legacy per-container policy semantics.
- **Concurrency safety:** an active chain reserves all of its members so manual checks/updates and the normal policy scanner cannot race the transaction. Interrupted controller runs are marked failed on restart instead of being ambiguously resumed.
- **Durable history:** Update History now records preflight and verification state/details. Chain definitions, steps, runs and run steps as well as verification profiles/states are stored in SQLite and included in the support bundle with sensitive verification details sanitized.
- **Backward-compatible migration:** V0.8.3.2 installations migrate additively. Existing containers default to `Not configured` verification and existing policies/automations continue without mandatory reconfiguration.

## V0.8.3.2 highlights

- **Remote-consistent Container Configs:** configuration inventories are fetched per host with endpoint-aware timeouts and rendered incrementally. Archived snapshots remain visible while a slow/unreachable VPN host refreshes, and live host data is preserved on failed refreshes.
- **Remote snapshot budgets:** pre-update snapshots use up to three minutes on TCP/remote hosts, manual snapshots up to four minutes, and dependent-container recovery snapshots inherit the same remote-aware budget.
- **Batched namespace dependency discovery:** candidate containers are inspected in one Docker call before a parent update instead of one inspect round-trip per container, reducing VPN latency while keeping fail-closed dependency safety.
- **Bounded rollback operations:** manual/full rollback transactions now have generous host-aware total deadlines and bounded safety-recovery/cleanup contexts so a broken tunnel cannot leave a job running forever.
- **Log consistency and retention:** Application logs are shown newest-first like Audit/Docker/Pushover. Audit history is now retained to the latest 5,000 events; Docker Events and Pushover already use 5,000-entry retention, while app.log uses 25 MiB rotation with five backups.
- **Dashboard clarity:** the global Containers card now shows running and offline counts.
- **UI polish:** the Containers digest explanation is a blue Info notice, Container Configs is narrowed to match the other tables, and the sidebar includes a GitHub link to `M9RPH/vibewatch` below Discord.
- **No database migration:** V0.8.3.2 remains compatible with the existing V0.8.3.1 data directory.

## V0.8.3 highlights

- **Remote-host tolerant loading:** container inventories refresh independently per host and keep their last successful rows visible while a slow or temporarily unavailable VPN host refreshes. Per-host refreshes are de-duplicated and throttled so the 8-second UI refresh cannot stack overlapping Docker requests.
- **Granular Check All progress:** `Check all on host` and `Check all · all hosts` now use the same per-container asynchronous check jobs as the single-container Check action. Every queued/running container exposes its own progress bar and stage directly in the table; the bulk banner is only a compact summary.
- **Hosts no longer block each other:** bulk checks run hosts independently and use a bounded maximum of two concurrent container checks per host. Worker readiness locking is per Docker host instead of global, so a slow VPN worker cannot stall checks on unrelated LAN hosts.
- **Fewer remote Docker round-trips:** container image labels/platforms are batch-inspected and cached, network inspect is batched, Docker system disk usage is collected once for image/build-cache data, redundant host-info calls are avoided, and the Dashboard fetches overview/volumes/networks in parallel.
- **Stale-while-refresh Dashboard:** host cards update independently, retain the most recent successful values across Dashboard navigation, and clearly show `Refreshing` / `Cached data` when a new collection is slow or fails.
- **Automation scan parity:** scheduled host policy scans also use bounded per-host concurrency instead of checking every eligible container strictly one after another.
- **No database migration:** V0.8.3 is compatible with the V0.8.2.1 data directory and does not change update/rollback persistence formats.

## V0.8.2.1 highlights

- **Consistent tables:** all actual table views now use the shared Vibewatch table visual system. Header labels align with the content below them instead of inheriting the browser's centered table-header default; explicit action columns stay right aligned.
- **Clearer recovery navigation:** the former **Container Backups** page is now **Container Configs**, while **Rollback** is now **Container Rollback**. The sidebar places Container Configs before Container Rollback and both pages explain their distinct recovery scope.
- **Correct retention-slot labels:** `Oldest` follows the actually oldest retained config snapshot. Empty retention capacity is shown as a free/available slot instead of making an empty third slot look like the oldest snapshot.
- **Cleaner host dashboard:** Inventory and Cleanup panels are stacked vertically inside each host card while their object/action tiles remain responsive grids.
- **No behavior/schema change:** Docker update, rollback, backup APIs and persisted data remain compatible with V0.8.2.

## V0.8.2 highlights

- **Stable namespace recovery Compose:** runtime `network_mode: container:<ephemeral-id>` values are normalized during snapshot creation. Same-project Compose relationships are reconstructed as `network_mode: service:<service>`; external namespace relationships fall back to `container:<stable-container-name>`.
- **No stale runtime hostnames:** namespace-sharing services no longer persist inherited/runtime hostnames or other Docker flags that conflict with container network mode. Docker-generated self-ID hostnames on ordinary containers are also omitted while explicit custom hostnames remain intact.
- **Fail-closed recovery artifacts:** if an ID-based namespace parent cannot be resolved to a stable service/container reference, Vibewatch refuses to create the recovery snapshot instead of storing a Compose file that would already be stale after a recreate.
- **No DB migration:** existing V0.8.1 data remains compatible; the correction applies to newly created recovery snapshots and Restore Points.

## V0.8.1 highlights

- **Network-namespace dependency transactions:** before updating a container, Vibewatch detects direct dependents using Docker Engine `HostConfig.NetworkMode=container:<id>`. This covers common Compose `network_mode: service:<name>` VPN/sidecar layouts without hard-coding Gluetun or any application name.
- **Correct dependent recreation:** when the parent receives a new container ID, Vibewatch verifies it first and then recreates every detected dependent against the new namespace. Previously stopped dependents are recreated but remain stopped; running dependents are started and verified.
- **Rollback integration:** dependency relationships and their retained runtime snapshots are stored with the full Restore Point. Rollback stops dependents first, restores and verifies the parent, then recreates them against the restored parent ID. A dependent failure makes the transaction fail instead of reporting a false successful parent update.
- **Retention-safe transactions:** cross-stack dependent snapshots remain pinned for as long as the parent Restore Point is retained, even if the dependent backup unit would otherwise exceed its configured retention. Expiring the parent transaction removes its transaction-only dependency snapshots.
- **Transparent history/audit:** Update History shows dependency count/status/names, the Container Rollback page shows retained namespace dependents, and structured audit/job events record detection and recreate start/success/failure.

## V0.8.0 highlights

- **Full container restore points:** before a non-Swarm container update, Vibewatch now combines the existing runtime/config recovery snapshot with a `docker commit --pause=true` capture of the container writable layer. The internal restore image is retention-managed and protected from Vibewatch image cleanup.
- **Automatic failed-update rollback:** if Watchtower replaces/breaks the target and the resulting container is missing, stopped, restarting or explicitly `unhealthy`, Vibewatch can restore the captured pre-update container automatically. Containers without a Docker healthcheck receive a short running-state stability window; Vibewatch does not invent application-specific health.
- **Digest-safe rollback:** after a successful manual or automatic rollback, the currently available remote digest is snoozed so an Auto Update policy cannot immediately reinstall the same bad image. A newer remote digest naturally clears the snooze through the existing digest-bound Snooze logic.
- **Dedicated Container Rollback page:** retained restore points show host/container, original image/version, protection level, mounted-volume/bind references, restore status/history and one-click rollback availability. The Containers page also exposes a direct Rollback action to the latest retained usable full restore point.
- **Retention as one recovery lifecycle:** when the linked recovery snapshot expires, the full restore point expires with it and Vibewatch untags its internal restore image. Original image, volume and network rollback protection continues to follow retained recovery artifacts.
- **Honest mounted-data boundary:** container writable-layer data is captured, but contents of Docker volumes and bind mounts are not copied into the restore image. Persistent database migrations therefore still require an application/database/volume backup when data rollback is needed. Docker Swarm remains configuration-only for one-click recovery.

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
- **Container Configs – Download all:** Admin/Owner can download the currently retained recovery snapshots across permitted hosts as one archive. The existing limit of three snapshots per stack/standalone service remains unchanged.
- **Dashboard attention summary:** the existing Dashboard now surfaces updates, unhealthy/restarting containers, offline hosts/workers, config drift, reclaimable images and unused anonymous volumes without introducing a second dashboard.
- Docker Events remain isolated in bounded `/data/logs/docker-events.jsonl`, and the V0.5.2 event-corruption recovery remains active.

## Policy semantics

| Policy | Scheduled policy run | Manual check | Manual update |
|---|---|---|---|
| Manual | Watchtower digest check; never install | Yes | Yes |
| Auto Update | Watchtower digest check; queue install when changed | Yes | Yes |
| Excluded | Read-only local/registry digest info only | Direct action blocked; background/scheduled read-only metadata refresh only | Blocked until policy changes |

The Watchtower workers themselves remain passive. V0.8.1 continues to start them with `WATCHTOWER_HTTP_API_PERIODIC_POLLS=false` and `WATCHTOWER_UPDATE_ON_START=false`. Updates are initiated only by Vibewatch manual actions, policy automation, worker maintenance, or the explicit Owner-only application self-update helper.

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

### Upgrade to V0.8.5

Copy the existing **entire `data/` directory** and `.env` into the V0.8.5 project directory and rebuild normally. V0.8.5 does not add a SQLite migration; existing V0.8.4.2 hosts, groups, chains, verification profiles, Restore Points, snapshots, policies, users and history remain compatible.

### Upgrade to V0.8.4.2

Copy the existing **entire `data/` directory** and `.env` into the V0.8.4.2 project directory, then run:

```bash
docker compose up -d --build
```

V0.8.4.2 applies an additive Update Chain migration: `update_chains` receives `scope_type`, `scope_key` and `policy_mode`, and `update_chain_steps` receives `current_action` (`skip` by default). Existing V0.8.4/V0.8.4.1 chains automatically become `Custom Chain` entries with inherited per-container policy behavior; no existing chain steps, chain history, policies or automations are discarded. New stack-scoped chains can instead own a single Manual/Auto Update/Excluded policy for all detected members of that Compose stack. Upgrades coming from V0.8.3.2 or older also receive the V0.8.4 verification/preflight/chain migrations. Keep the complete `/data` directory, including `registry-credentials.key` when private registry credentials are configured.

### Upgrade to V0.8.4.1

V0.8.4.1 used the V0.8.4 SQLite schema. When upgrading through V0.8.4.2 the additive stack-chain columns are applied automatically; no manual migration step is required.

### Upgrade to V0.8.3.2

Copy the existing **entire `data/` directory** and `.env` into the V0.8.3.2 project directory, then run:

```bash
docker compose up -d --build
```

V0.8.3.2 is backward-compatible with the V0.8.2.1/V0.8.2/V0.8.1/V0.8.0 data directory (and therefore the earlier supported upgrade chain). No new SQLite migration is required for V0.8.3.2; existing hosts, users, groups, policies, automations, Pushover settings, recovery snapshots, Restore Points, Config Drift baselines and logs remain compatible. Existing pre-V0.8.0 history remains available through the legacy image/config rollback path while V0.8.x updates create full restore points. Keep the entire `/data` directory when upgrading. Once private registry credentials are configured, `/data/registry-credentials.key` is required together with `vibewatch.db`; do not migrate only the database file. Database backups created by Vibewatch also preserve a companion registry-key copy when one exists.

## Publishing later

The included GitHub Actions workflow builds `linux/amd64` and `linux/arm64` images and publishes to GHCR on `main`/`v*` pushes. The default branch also receives `latest`, making it suitable as the future `WTUI_APP_IMAGE` self-update channel.

## Current limitations

- GitHub patch notes require a detectable/configured GitHub repository; non-GitHub projects can still use registry version information and Watchtower update detection.
- Full-container rollback is available for Compose/standalone containers only while both the linked recovery snapshot and committed restore image remain retained. Swarm restore points remain configuration-only. Docker volume and bind-mount contents are not copied into the committed restore image, so application/database data migrations still need their own data backup.
- Automatic rollback reacts to concrete Docker runtime failure signals (missing/stopped/restarting or a Docker healthcheck that remains `unhealthy` through its bounded post-update grace window) and, when configured, failed V0.8.4 Custom Verification. Containers without a Docker healthcheck still receive a short running-state stability gate. Without a custom verification profile Vibewatch intentionally cannot infer application-level correctness and reports `Not configured`.
- Network-namespace dependency handling currently covers direct Docker Engine `container:<id>` relationships only. It deliberately does not attempt to infer every possible application, startup-order or data dependency. Recreating a dependent follows normal Docker recreate semantics: mounted volumes/binds persist, while application data written only to that dependent container's writable layer should not be treated as a persistent datastore.
- Custom HTTP/HTTPS/TCP verification runs from the Vibewatch controller network namespace. Targets must therefore be reachable from the controller; HTTPS uses normal certificate validation and V0.8.4 does not add an insecure-TLS bypass.
- Remote Docker Engine APIs do not provide a universally reliable way to stat arbitrary host bind paths or the host filesystem's free capacity. Preflight reports those unverifiable remote conditions as explicit warnings rather than false green checks; missing Docker volumes, manifest/platform failures and recovery-preparation failures remain blocking.
- Update Chains bind optionally to an existing Automation policy run and therefore reuse its cron schedule, host/group target and pause state. V0.8.3.2 had no separate persisted duration-style maintenance-window model, so V0.8.4 does not introduce a parallel maintenance-window subsystem. A controller restart marks an active chain failed instead of attempting an ambiguous automatic resume; retained Restore Points/history remain available for operator recovery.
- The application self-update path only becomes meaningful once the controller itself is deployed from a registry image rather than a locally built tag.
