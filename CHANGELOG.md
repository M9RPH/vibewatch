# Changelog

## 0.7.2.1

- Dashboard host inventory tiles now place their `Open` action directly below the object count, preventing overlap with the Images / Volumes / Networks counters on narrow cards.
- No API, database, cleanup or inventory logic changes.

## 0.7.2

- Reorganized the **Hosts & groups** page into a two-column management layout: configured Docker hosts now occupy the main left workspace, while the right-side management column is ordered as **SSH Quick Setup → Add Docker host → Create host group → Host groups**.
- Kept all existing host, group and SSH setup behavior unchanged while adapting form widths and spacing for the narrower management column.

## 0.7.1

- Added **retention/rollback protection for Docker volumes**. Volumes referenced by retained pre-update snapshots expose the protection state and retained-snapshot count and cannot be deleted by Vibewatch cleanup.
- Replaced broad anonymous volume pruning with **verified individual deletion**, allowing rollback-protected anonymous volumes to be excluded safely.
- Strengthened per-host **Volume inventory verification**: normal all-container inspection remains the fast path; incomplete scans fall back to Docker's per-volume container filter and expose the concrete referencing container names.
- Added Compose origin metadata and explicit host/endpoint inventory-source information to the Volume dialog so inventory results can be cross-checked against Docker/Portainer.
- Dashboard inventory failures now display as **unavailable** instead of silently looking like zero Volumes/Networks.
- Reworked per-host Dashboard actions into consistent **Inventory** and **Cleanup** groups with uniform counters/status context for Images, Volumes, Networks and Build Cache.

## 0.7.0

- Added digest-bound **Snooze** per container. The currently detected remote digest is skipped by manual/automatic update execution and update-available notification flow until a newer digest appears; the next digest automatically clears the snooze.
- Added persistent **First detected** timestamps for the currently pending remote digest and surfaced them in the Containers table.
- Made unused-image cleanup **rollback aware**: retained pre-update snapshots protect referenced image IDs, and cleanup now deletes only eligible unused image IDs individually instead of using blanket `docker image prune -a`.
- Added **edit, pause and resume** controls for policy-run automations without requiring deletion/recreation.
- Added Owner-only configurable **Container recovery retention** (1–20 snapshots per stack/service, default 3), with immediate pruning of oldest excess snapshots when lowered.
- Added per-host **Docker Networks** inventory and safe cleanup for unused custom networks; default/in-use/worker/ingress, Swarm-scoped and rollback-protected networks are excluded.
- Added per-host **Docker Build Cache** usage/reclaimable metrics and explicit Admin/Owner cleanup.
- Added additive cache persistence/migrations and regression tests for snooze/first-detected state transitions.

## 0.6.1

- Reworked **Config Drift** so recovery snapshots and drift baselines are separate concepts. Successful updates and rollbacks now refresh a secret-safe runtime baseline, preventing normal image updates from creating false drift. Existing V0.6.0 post-update drift rows are rebaselined once during refresh.
- Hardened per-host **Volume inventory** by batching Docker inspect calls, falling back to per-volume/per-container inspection, retaining volumes whose metadata cannot be read, and refusing to classify incomplete usage data as unused.
- Added backend and UI safeguards so volumes with unknown usage or failed metadata inspection cannot be explicitly deleted as named unused volumes.
- Increased wide Image/Volume inventory dialogs from 920 px to **1070 px**.
- Rounded the Dashboard **Needs attention** metric tiles to match the rest of the interface.
- Removed the redundant Dashboard **Available updates** and **Policy model** panels.
- Reworked the sidebar brand: larger logo with **Vibewatch** underneath when expanded, compact logo when collapsed, and removed the “Docker Update Control” subtitle.

## 0.6.0

- Added persistent per-container **Update History** with trigger, actor, version/image transition, duration, result and linked pre-update recovery snapshot.
- Added **manual rollback** from eligible successful update-history entries. A fresh safety snapshot is created before rollback; automatic health-based rollback is intentionally not enabled.
- Added cached **Config Drift Detection** against the latest recovery snapshot with per-field differences surfaced on the Containers page and a Dashboard attention count.
- Added Owner-only **private registry credentials** for authenticated read-only Docker Hub/GHCR/custom OCI manifest and version checks. Secrets are AES-256-GCM encrypted with a persistent key under `/data` and excluded from API/support outputs.
- Added **Download all backups** for the retained Container Backup recovery packages; existing three-snapshot-per-stack/service retention remains.
- Added a compact **Needs attention** section to the existing Dashboard.
- Restored structured Docker/Application log rendering helpers and kept V0.5.2 file-backed Docker event isolation.
- Added companion backup handling for the registry-credential encryption key when present.


## 0.5.2

- Moved high-volume Docker event history out of the primary `vibewatch.db` into bounded `/data/logs/docker-events.jsonl` storage so diagnostic event traffic cannot corrupt application state.
- Added startup recovery for the V0.5.0/V0.5.1 event-table corruption failure mode: Vibewatch verifies every core table independently, preserves the damaged database plus WAL/SHM sidecars, rebuilds only healthy application state into a clean database, and discards the corrupted Docker-event history.
- Docker event history is capped at 5,000 retained entries and noisy `exec_create` / `exec_start` / `exec_die` healthcheck events are no longer persisted.
- Healthy upgrades migrate up to 500 readable legacy Docker events into the new JSONL store; corrupted legacy event history is intentionally skipped.
- Reworked the Container Backups table to use the same semantic table surfaces, borders, filter-row styling, and Light/Dark theme tokens as the Containers table.
- Replaced hard-coded dark snapshot-slot backgrounds with theme-aware backup-slot styles, eliminating grey/black blocks in Light Mode.
- Added regression tests for JSONL event persistence/noise filtering and isolated Docker-event database recovery.

## 0.5.1

- Added per-column filtering to Container Backups.
- Added `Snapshot all services` to create one recovery snapshot for every live stack/standalone service sequentially with an on-page progress bar and success/failure counts.
- Reworked snapshot history into three persistent table slots (Latest / Previous / Oldest) with ZIP and Compose downloads always visible in-context.
- Dashboard volume controls now mirror image controls with a live volume count and direct safe cleanup of unused anonymous volumes.
- Widened Image and Volume inventory dialogs by roughly 150 px for better table readability.
- Moved Container Backups directly below Containers in the sidebar.
- Fixed diagnostics robustness: empty Docker event/delivery queries now serialize as arrays, and Docker Events / Pushover Delivery are loaded independently so one failing source cannot blank the other.
- No new SQLite schema migration is required; existing `/data` remains directly reusable.


## 0.4.9

- Restored policy-color semantics directly on the Containers policy dropdown: Manual is blue, Auto Update is green, and Excluded is yellow.
- Added matching Light/Dark mode treatments while preserving the V0.4.8 digest-based status model.
- No database or worker migration is required.

## 0.4.8

- Separated immutable Docker image digest state from human-readable version metadata throughout the Containers UI.
- Added explicit `New image available` status when the digest changed but installed/latest version metadata is incomplete or `Unknown`.
- Added `Check unavailable` status for registry/digest/worker lookup failures instead of implying that an image is current; failed active checks now persist that state for the UI.
- Shows shortened current/latest SHA-256 digests under `Unknown` version values so update decisions remain explainable.
- Added read-only registry digest checks for Excluded containers. Excluded images are never pulled and containers are never stopped/recreated/updated; the local Docker image-config digest is compared with the matching platform-specific remote manifest config digest.
- Excluded read-only information is refreshed on the Containers page and during scheduled/bulk policy scans while update actions remain blocked until the policy changes.
- Improved digest-pinned image parsing so an explicit `@sha256:...` reference is treated as immutable rather than accidentally following `latest`.
- Added `container-update-state.json` to support bundles for safe digest/version diagnostics without credentials.
- Added architecture-aware registry resolution so ARM/ARM64 and amd64 hosts compare against their own platform manifest and read the correct OCI version labels; changes to another architecture no longer create false Excluded update signals.
- Added registry and Docker digest regression tests plus updated policy/version documentation.

## 0.4.7

- Added live progress tracking for manual per-container update checks. Checks now return a job immediately and expose real backend job status to the UI.
- Added live progress tracking for manual container updates with queued, worker preparation, update-engine, verification and final success/failure stages.
- Added `/api/job-status/{id}` for permission-filtered job progress polling.
- Added a host-by-host progress panel for manual `Check all` runs, including checked containers, updates found and host errors.
- Container action controls are replaced by an inline progress bar while a manual check/update is active, preventing duplicate actions and policy changes during the operation.
- Dashboard container-memory telemetry now displays `Unavailable` when Docker returns no usable aggregate memory usage instead of showing a misleading `0 B`.
- Support-bundle review confirmed all four workers healthy after V0.4.6 startup, 101/101 recorded jobs successful, and the manual Pushover delivery path successfully recorded/delivered.

## 0.4.6

- Fixed a frontend production-build regression in V0.4.5 where the `Users` page component was accidentally omitted while the route still referenced it.
- Restored the full Users & permissions page from the working V0.4.4 implementation, including Owner/Admin/User management and permission editing.
- V0.4.5 worker isolation, manual-update Pushover notifications and Pushover delivery logging remain unchanged.
- Restored a frontend typecheck path that specifically catches unresolved page-component references such as this regression.

## V0.4.5

- Isolated centrally hosted remote Watchtower workers from the local-host worker so the local worker cannot remove sibling Vibewatch workers during Watchtower instance cleanup.
- Added per-account `Notify me after manually performed updates` Pushover preference with success/failure delivery.
- Added persistent Pushover delivery history and a readable Pushover Delivery tab under Logs, plus delivery records in support bundles.


## V0.4.4

- Replaced the shared/global Pushover credential model with fully per-account credentials. Every Owner/Admin/User now stores its own Pushover Application API Token/Key plus its own Pushover User Key and preferences.
- Pushover delivery uses the recipient account's own App Token + User Key pair after host-permission filtering; credentials are never borrowed from the Owner.
- Settings now expose both personal Pushover fields to every signed-in account, with masked App Token state, replace/clear controls, and save-and-test using the current account's credentials.
- Added additive SQLite migration for `notification_settings.pushover_app_token`; App Tokens are backend-only and never serialized in API responses or support bundles.
- Added upgrade migration that imports a legacy shared persistent/environment App Token into the Owner account only when the Owner has no personal token. Admins and Users never inherit it.
- Support bundle Pushover diagnostics now report only per-account configured/not-configured state and preferences, never credential values.
- No worker protocol/config compatibility change.

## V0.4.3

- Fixed sidebar title/subtitle collision with the collapse button.
- Added Owner-only SSH Quick Setup for Linux/systemd Docker hosts, including one-time password handling, host-key persistence, explicit insecure-TCP acknowledgement, Docker restart/health validation, rollback, endpoint verification, automatic host creation and worker launch.
- Added host address/IP to Dashboard host information.
- Reworked lower-right toast notifications for strong Light/Dark contrast.
- Made the Pushover.net application API token persistable from Settings with environment fallback and runtime reload; clarified application-token vs per-user User Key configuration and made the test action save-and-send in one step.
- Added Pushover configuration-source diagnostics to support bundles without exposing secrets.
- Corrected Docker RAM-capacity selection to prefer daemon `MemTotal` and reject unlimited cgroup sentinel values from stats.

## V0.4.2

- Added a permanent **Vibewatch Discord** link to the bottom of the desktop sidebar using the Discord logo and invite `https://discord.gg/ZpXxngq`; the collapsed sidebar keeps an icon-only version with hover label.
- Fixed Admin account visibility: an Admin now sees the Owner, their own Admin account, and all normal User accounts under **Users → Managed accounts**. Peer Admin accounts remain Owner-only.
- Admins can change their own password without being able to change their own role/enable state, and can edit User host/group assignments as intended by the Owner → Admin → User hierarchy.
- Hardened Docker RAM capacity discovery: Vibewatch now accepts numeric or human-readable Docker `MemTotal` output and cross-checks it with the memory limit reported by `docker stats`, choosing the largest Docker-derived credible capacity value.
- Added `memory_source` and `memory_diagnostic` to host-overview data so RAM discrepancies are explainable instead of opaque.
- Support bundles now include `host-overviews.json` with the same Docker host metrics shown on the Dashboard, including RAM source/diagnostic data.
- No worker protocol/config compatibility change.

## V0.4.1

- Managed Accounts now includes the environment/bootstrap **Owner** account. The Owner can change its password from the UI; the new PBKDF2 hash is persisted under `/data` and takes precedence over `WTUI_ADMIN_PASSWORD` without attempting to rewrite `.env`.
- Added per-column filters to the Containers table for container/image, host, stack/service, installed version, available version, status and policy.
- Reordered Dashboard content so Docker host health/storage appears before Available Updates and the Policy model.
- Hardened Docker RAM/CPU capacity discovery with direct daemon queries for `.MemTotal` and `.NCPU` to avoid CLI JSON-template inconsistencies.
- The one-shot `vibewatch-runtime-migrate` helper is now removed automatically a few seconds after a successful controller start.
- Graceful controller shutdown now removes all dynamically created `vibewatch-worker-*` / legacy worker containers, so `docker compose down` leaves the deployment clean and allows the Compose network to be removed.
- Added a 30-second Compose stop grace period for worker cleanup.
- Added host rename support on the Hosts page. Renaming changes only the display name; Host ID, endpoint, worker token, groups, policies and worker binding remain unchanged.
- Added regression coverage for managed-worker cleanup and direct Docker daemon capacity parsing.
- No worker protocol/config compatibility change.

## V0.3.9

- Renamed the Docker controller container to `vibewatch`.
- Renamed centrally managed workers to `vibewatch-worker-<host-id>`.
- Renamed the owner-only self-update helper to `vibewatch-self-updater`.
- Renamed the controller/worker bridge network to `vibewatch-internal`.
- Renamed the runtime binary to `/usr/local/bin/vibewatch`.
- Added automatic removal/recreation of legacy `watchtower-ui-worker-*` containers while preserving each host's stored endpoint and worker API token.
- Added best-effort cleanup of the legacy `watchtower-ui-internal` network after worker migration.
- Migrates `/data/watchtower-ui.db` atomically to `/data/vibewatch.db` on first start, preserving all persistent state.
- Renamed the Compose service to `vibewatch` and added a short-lived `vibewatch-runtime-migrate` service so a normal `docker compose up -d --build` removes only legacy runtime objects before the new controller starts.
- Retained legacy `WTUI_*` environment variables for `.env` compatibility.

## V0.3.8

- Dashboard `Available updates` entries now show the effective container policy (`Manual`, `Auto Update`, or `Excluded`).
- Excluded containers remain visible in the update overview when a previously detected update is known.
- No worker compatibility changes; existing healthy workers are not recreated.

## V0.3.7

- Replaced the Vibewatch application logo with the new supplied artwork across sidebar, login screen, favicon and Apple touch icon.
- No worker/runtime compatibility change; healthy workers are not recreated for this branding-only release.

## V0.3.6

- Product branding changed to **Vibewatch** in the web UI, page title, container metadata, support bundle and notification test.
- Retained legacy internal service/container/env/database identifiers for safe in-place upgrade compatibility.
- Dashboard available-update entries now include host, image, stack and version context.
- Removed the outdated Container-page policy reminder.
- Vibewatch controller and Watchtower workers are now system-managed inventory rows: muted, non-interactive, excluded from bulk/automation scans, and rejected by manual policy/check/update APIs.
- Application logs are parsed from structured JSON into Time / Level / Event / Context rows.
- Docker events are parsed into Time / Host / Event / Container-Image rows with expandable raw payload.
- Persistence explanatory panel now follows Light/Dark design tokens.
- Added Settings credit panel for Nicholas Fedor's Watchtower fork and worker image.
- Database backups created by this version use a `vibewatch-*.db` prefix while retention remains compatible with older `watchtower-ui-*.db` backups.

## V0.3.5

### UI / design consistency
- Reworked the Containers table around a dedicated semantic table design instead of mixed Tailwind utility colors.
- Removed the sticky table header that could overlap the first data row while scrolling.
- Light and dark themes now use matching table surfaces, borders, row states and headers; the light theme no longer renders a black table header.
- Added fixed column proportions and a controlled 1210 px minimum table width; narrower viewports scroll horizontally instead of crushing cell contents.
- Actions are arranged in a compact 2×2 grid, giving Check/Update/Notes/Source equal visual weight without widening the table excessively.
- Tightened secondary typography while keeping container names, versions, status and policies visually dominant.
- Added subtle alternating rows and hover feedback instead of heavy gray row blocks.
- Standardized shared Cards, Buttons and Badges with common surface/border/radius/typography rules across the application.
- Increased the main desktop content canvas to 1720 px so data-heavy pages can use available space more effectively.
- Stack/service information remains visible as a dedicated table column.

### Compatibility
- No backend schema or worker compatibility change. Existing healthy workers, database state, users, policies, groups and automations are preserved.

## V0.3.4

- Returned the Containers page from card layout to a compact tabular configuration view.
- Kept stack/service relationships, readable versions, policy controls and per-container actions in the table.

## V0.3.3

### Persistence / upgrade safety
- Added `WTUI_DATA_PATH` to Compose (`./data` by default) so operators can pin `/data` to a stable host path outside a checkout.
- Owner Settings now verify the live `/data` Docker mount and show mount type/source/read-write status.
- Added consistent SQLite backups via `VACUUM INTO`.
- Existing databases are backed up before startup/schema migration; newest five startup snapshots are retained.
- Manual and scheduled application self-update create a database backup before launching the self-update helper; newest ten explicit/update backups are retained.
- Added Owner-only manual database backup endpoint/button.

### UI
- Added collapsible desktop sidebar with smooth 256 px → 80 px transition.
- Compact sidebar shows only icons/status indicators; navigation names, worker/controller state and account/version information appear on hover.
- Sidebar preference persists in browser localStorage.

### Compatibility
- Worker compatibility version is unchanged; existing healthy Watchtower workers are preserved.

## V0.3.2

- Redesigned the Containers page to remove the compressed wide table and separate identity, stack, version/update information, policy, and actions into responsive cards.
- Added Docker stack discovery from container labels: Docker Compose/Portainer Compose project + service and Docker Swarm stack + service.
- Containers are grouped visually by host and stack; containers without stack labels are shown as standalone.
- Removed repository icons beside container names and from the Source button.
- Reordered Hosts & groups: Add Docker host and Create/Edit host group are at the top; host status and group overview follow below.
- Support-bundle review for the V0.3.1 test showed no controller errors; both workers were running. One harmless Watchtower startup warning (`Current container not cached for cleanup`) was observed before normal API startup.
- Worker compatibility version remains unchanged, so healthy workers are not recreated for this UI/API update.

## V0.3.1

### Fixed
- Host groups with no members now always return `host_ids: []` instead of omitting the field.
- The Hosts page defensively normalizes legacy/malformed empty assignment arrays, preventing the blank-page crash after creating an empty host group.
- User assignment arrays are also normalized so empty host/group permissions remain editable and never break rendering.

### Changed
- Automation layout restored to a clearer two-column design below the three policy counters: configured policy runs on the left, new policy run on the right.
- User management now makes post-creation permission editing explicit with **Edit permissions**, group/direct-host selection and an effective-access preview.
- Host-group editing is clearer, supports empty groups, and shows an explicit `No hosts assigned` state.
- The supplied Watchtower UI whale/watchtower logo is now bundled as `web/public/logo.png` and used for branding/favicon.

### Diagnostics
- Added a React page error boundary so one render error no longer turns the entire content area blank.
- Browser render/unhandled errors are posted to the controller application log and therefore appear in future support bundles.

## V0.3.0

### Added
- Controller-managed automatic Watchtower worker image maintenance.
- Configurable Owner-only worker maintenance schedule, default daily 03:30.
- Sequential worker recreation preserving each host's ID, Docker endpoint and worker API token.
- Public Docker Hub/GHCR/OCI registry metadata resolver for readable target versions.
- `latest_source` version metadata in the UI/database.
- Pushover notifications with one global app token and per-account user keys/preferences.
- Permission-filtered Pushover recipients for Manual update availability and automatic update outcomes.
- Owner / Admin / User hierarchy.
- Owner-only system maintenance controls.
- App self-update plumbing for future published registry images.
- Container-table source indicator showing whether a custom GitHub release repository is configured.
- Branding slot at `/logo.png` with graceful built-in fallback.
- GHCR workflow `latest` tag on the default branch.

### Security / behavior
- Workers explicitly keep `WATCHTOWER_HTTP_API_PERIODIC_POLLS=false` and `WATCHTOWER_UPDATE_ON_START=false`.
- Worker maintenance takes an exclusive worker-operation lock before recreation.
- Application self-update targets only the local `watchtower-ui` controller container.
- User notification routing is evaluated server-side against effective host permissions.
- Disabled managed users lose session access on subsequent protected requests.
- Deleted users/hosts clean related notification state.

### Changed
- Environment-backed root login remains `admin`, now represented as role **Owner**.
- Admin is a global operational role; User is scoped by assigned hosts/groups.
- Version display prefers registry OCI metadata before GitHub release fallback.

### Notes
- Public registry metadata is supported in V0.3.0; private registry metadata credentials are a future enhancement.
- Actual custom logo file was not bundled if `web/public/logo.png` is absent; the UI is already wired for it.