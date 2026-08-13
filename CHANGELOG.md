# Changelog

## v0.9.2.7

- Refined the Containers table proportions: the Image column is narrower and Stack / Service gets more room while filter/table alignment stays exact.
- Rebalanced Update Chains so the Create/Edit editor gets more horizontal room and Configured Chains / Chain History remain the primary left column.
- Reworded Preflight help text as user-facing safety guidance and retained the immediate live progress stream.
- Reworked container drawer actions into two consistent action groups with equal-size controls, and normalized Safety & Verification badge height.
- Corrected remaining visible/default V0.9.2.6 strings to the unchanged V0.9.2.7 release line.
- Containers UX hotfix: removed the dedicated **Status / Attention** column; runtime remains the state dot, Config status is shown beneath the container identity, and Verification status remains available in the details drawer.
- Containers table/filter alignment now uses a wider six-column grid: Container, Host, Stack / Service, Image, Policy and Actions.
- Compact row actions now use a check icon for **Check**, a download/update icon for **Update**, and the existing ellipsis for the details drawer; accessible labels/tooltips are retained.

- UI hotfix: restored dedicated **Host** and **Stack / Service** columns on the Containers page; filters now align to the same seven-column grid.


- UI alignment pass: Containers filter controls now use the exact same six-column proportions as the table below.
- Host / Stack / Service context redesigned as a visible hierarchy rather than equal-weight label rows.
- Container Rollback now has explicit aligned table headers; filters, headers and expandable restore-point summaries share one grid.
- Configured Chains and Chain History now have explicit aligned table headers and matching row grids while retaining progressive disclosure.
- No update, rollback, policy, transaction or database logic changed.

## 0.9.2.6

- Unified table/filter alignment across Containers, Container Rollback and Update Chains; control insets now match table/disclosure headings and rows.
- Container row ellipsis now opens the existing right-side detail drawer instead of a floating action popover.
- Dashboard host cards remain compact and use the same ellipsis + right-side drawer pattern for full host metrics, inventory and cleanup actions.
- Host / Stack presentation now clearly distinguishes Host, Stack and Service instead of relying on font weight alone.
- Image update wording is consistently `Update available`.
- Added informational update classification: Major, Minor and Patch are derived from readable version metadata; Security is shown only when matching release metadata explicitly identifies a security/CVE fix. Digest comparison remains authoritative.
- Status/Attention badges now include their domain (`Runtime`, `Verification`, `Config`) so `Not configured` is no longer ambiguous.
- Container Rollback disclosure rows and filters share a single aligned shell.
- Configured Chains and Chain History are displayed side-by-side on wide screens and use aligned compact disclosure shells.
- Added additive `version_cache.update_kind` and `version_cache.security_update` migration; existing databases remain backward-compatible.


## 0.9.2.5

- Consolidated per-container row actions to `Check`, `Update` and a contextual `More` menu while preserving queued/running operation progress and cancellation.
- Added a right-side Container Details drawer for image/digest metadata, placement, policy, verification, config drift, restore-point integrity and secondary actions.
- Added quick filters for Needs Attention, Updates, Snoozed, Config Drift and Chain Managed containers and reduced the main container table to a clearer six-column information hierarchy.
- Converted Configured Chains, Chain History and Container Rollback records to compact expandable disclosure cards so detailed ordering, recovery and transaction information is shown on demand.
- Simplified Jobs to a compact execution overview while keeping full persisted pipeline/job details in the existing row detail view.
- This is a UX-only release: no SQLite migration and no changes to the V0.9 transactional update, rollback, TLS/mTLS or CI/CD architecture.

## 0.9.2

- Added Owner-managed Docker TLS/mTLS host connections without changing the central controller/worker architecture. `tls://` endpoints are executed with Docker `--tlsverify` plus per-host CA/client certificate/private-key files; centrally managed workers receive the same credentials through a host-specific read-only mount. Existing Local Socket and TCP behavior remains compatible; SSH Docker transport and TLS Quick Setup are intentionally deferred.
- Added TLS/mTLS connection badges to Hosts/Dashboard and Owner-only certificate rotation. TLS secret material is stored under persistent `/data/host-tls` with restrictive filesystem permissions and is excluded from normal Host API/support-bundle serialization.
- Switched the default Compose deployment to the pinned published image `ghcr.io/m9rph/vibewatch:0.9.2`; added `docker-compose.build.yml` plus Make targets for explicit local source builds.
- Added a CI quality-gate workflow covering Go tests/vet/build, real frontend production build, Docker image build, release data-skeleton validation, Docker integration tests and the NetEm/VPN regression suite. Container publishing is validation-gated and produces `linux/amd64`/`linux/arm64` GHCR images.
- Added complete Owner backup bundles with transaction-consistent SQLite state, manifest/SHA256, registry-credential encryption key and configured Docker TLS/mTLS credential material. Bundles can be listed, downloaded, deleted and validated; restore is deliberately not implemented in this release.
- Added RBAC-aware JSON/CSV exports for Update History, Jobs, Audit, Docker Events and Pushover deliveries plus TXT export for the Admin/Owner Application Log.
- Added the read-only `Why didn't this update?` diagnostic on Containers, explaining effective policy/Stack Chain ownership, Automation availability, host reachability, active jobs/leases, snoozed/current digest state and the latest blocking Preflight result without triggering Docker/registry mutation.
- Prevented horizontal Sidebar scrolling in expanded/collapsed modes and added Buy Me a Coffee alongside the existing Discord/GitHub links.
- Restored the complete tracked release data skeleton (`data`, backups/bundles/containers, logs and host-tls) and based `.gitignore` on the supplied current repository file with only `.gitkeep` exceptions needed to preserve those directories.
- Added v0.9.2 regression coverage for endpoint normalization, unsupported SSH rejection, PEM client-certificate validation, backup filename traversal protection and Docker TLS CLI argument construction.
- No SQLite schema migration is required.

## 0.9.1

- Added persistent asynchronous Dashboard cleanup jobs for Images, anonymous Volumes, Networks and Build Cache. Cleanup is no longer tied to the browser/reverse-proxy request lifecycle, so a long-running remote/VPN cleanup continues server-side even when the initiating HTTP connection closes.
- Added granular cleanup progress stages and per-object counters to Dashboard cleanup tiles. Image, anonymous-volume and network cleanup report inventory/removal/refresh progress; build-cache cleanup reports measurement, prune and refresh stages through the existing Job progress stream. Active cleanup progress is recovered after a page refresh from persistent queued/running Jobs.
- Hardened cleanup for remote Docker endpoints with a 30-minute background job budget, longer bounded inventory/object timeouts for TCP/VPN hosts, and per-object failure isolation so one slow image/volume/network operation does not cancel the remaining cleanup list.
- Preserved cleanup API compatibility: Dashboard uses the new `async=1` mode, while legacy callers may still wait for the historical `{job_id,result}` response; disconnecting a legacy request no longer cancels the underlying cleanup.
- Fixed the NAS2/VPN failure pattern from the support bundle where a large image cleanup was cancelled after roughly 90 seconds by the HTTP request context, causing all remaining `docker image rm` calls and the final `docker info` refresh to fail with `context canceled`.
- Reworked the desktop Sidebar into a flex layout with a scrollable navigation region and non-overlaying controller/worker/user footer. On shorter screen heights the menu scrolls instead of being covered by the status box.
- Added cleanup progress/remote-timeout regression coverage. No SQLite schema migration is required.

## 0.9.0

- Added durable per-update transactions with an explicit persisted state machine (`queued → preflight → snapshot → restore_point → prepared → updating → docker_health → dependencies → verifying → refreshing → success`, plus rollback/recovery states). Jobs expose the current transaction stage and transaction history is retained independently from transient in-memory workers.
- Added controller crash recovery for interrupted update transactions. Pre-mutation interruptions are safely aborted; post-mutation interruptions reconcile the live runtime, network-namespace dependents and Custom Verification before either keeping the healthy updated runtime or invoking the existing full Restore Point rollback engine. Persisted leases owned by the interrupted job are reclaimed safely on startup.
- Added hierarchical persistent operation leases. Container update/rollback/chain lifecycle operations can run independently across containers, while host-scoped image/volume/network/build-cache cleanup is mutually exclusive with every container mutation on that host. Manual rollback is also blocked while an Update Chain reserves the target.
- Added Restore Point integrity validation for the retained Config Snapshot, writable-layer image, dependency snapshots, named volumes and custom networks. Integrity is checked immediately before full rollback and periodically by Recovery GC; degraded/expired points are blocked before destructive work.
- Added central Recovery GC with six-hour scheduled reconciliation and manual Admin/Owner execution. It applies existing snapshot retention, validates Restore Points, heals eligible recovered points, expires broken snapshot relationships, prunes old reliability history and removes only orphaned Vibewatch-owned `vibewatch-restore/host-*` images.
- Added Recovery Storage/Integrity visibility to Container Rollback, including protected image/volume/network counts, last GC result, per-Restore-Point integrity state and a manual `Run recovery GC` action.
- Enriched Update Preflight diagnostics with source, per-check duration metadata and explicit blocking classification. Critical transaction-state persistence failures now block an update before the running container can be mutated.
- Added durable Verification History (latest 5,000 runs) with trigger, transaction link, total duration and per-check timings; the Containers verification editor now shows the ten latest runs.
- Added Update Transaction visibility to Jobs so an update job exposes its durable pipeline state and recovery action instead of only its coarse queued/running/success state.
- Added a Docker integration test lab: `make test-integration` exercises health warm-up and the stale `container:<id>` namespace/recreate regression against a real Docker daemon; optional `sudo make test-netem` creates isolated network namespaces with ~50 ms RTT and configurable packet loss using `tc netem`.
- Added SQLite migration tables `update_transactions`, `update_transaction_events`, `operation_leases`, `verification_history` and `recovery_gc_runs`, plus Restore Point integrity columns. Existing V0.8.5 data migrates additively without mandatory reconfiguration.

## 0.8.5

- Unified the Update Chain member surfaces with the shared light/dark design tokens so detected stack members, order badges and member controls no longer render as inconsistent grey blocks in Light Mode.
- Added explicit deletion for `Archived only` Container Configs. Deleting an archived config removes its retained snapshots, expires linked Restore Points/object protection, and refuses deletion while a snapshot is pinned by another retained dependency rollback transaction.
- Reworked Dashboard host presentation into compact expandable host cards. Collapsed cards show the essential Docker/container/worker snapshot; expanded cards retain the existing full runtime, storage, inventory and cleanup controls.
- Grouped Dashboard hosts by their exact Host Group membership without aggregating or mixing per-host metrics. Hosts with multiple group memberships are shown once under that combined membership; ungrouped hosts remain in their own section.
- Added restrained Dashboard icons to overview, attention, host, inventory and cleanup headings for quicker visual scanning.
- Moved `Host groups` from the Hosts page right-side setup column into the main left workspace directly below configured Docker hosts. SSH Quick Setup, Add Docker Host and Create Host Group remain in the right-side tools column.
- Made post-update Docker Health verification tolerant of slow application warm-up. A transient `unhealthy` result is polled through a bounded grace window (minimum 45 seconds, derived from Docker health start-period/interval/timeout, capped at 2 minutes); only persistent unhealthy state after that window triggers the existing rollback path. Stopped/dead/restarting states remain immediately critical.
- No SQLite schema migration is required.

## 0.8.4.2

- Chain hotfix: each step can now choose `Skip`, `Restart` or `Recreate` when that member is already current. The lifecycle action is only eligible when the same chain run has at least one actionable image update; a completely current/snoozed chain remains a true no-op with no restart/recreate and no Pushover completion message.
- `Recreate if current` uses the existing Config Snapshot, full Restore Point, Network Namespace dependency rebinding, Docker Health and Custom Verification paths. If the recreate fails, Vibewatch restores the original container; retained recreate Restore Points also participate in `Rollback completed members`.
- Added additive `update_chain_steps.current_action` migration with backwards-compatible `skip` default for all existing chains.
- Tightened the Create Update Chain layout with a bounded form width so the editor no longer stretches across the entire page after the vertical page layout hotfix.
- UI hotfix: reordered Update Chains vertically as `Create update chain` → `Configured chains` → `Chain history`, reordered the sidebar to the operational flow Dashboard → Hosts → Containers → Container Configs → Container Rollback → Update Chains → Automation → Jobs → History → Logs → Users → Settings, and corrected visible web UI version labels to V0.8.4.2.
- Added Stack-scoped Update Chains that use the Docker Compose stack already detected by Vibewatch as the membership source. Selecting a stack imports all current non-system stack members while keeping update order and per-step waits explicitly user controlled.
- Added `Sync stack` and fail-closed live-membership validation. A Stack Chain cannot run when services were added/removed since its last sync; the same host/stack cannot be owned by multiple Stack Chains.
- Added chain-level `Manual`, `Auto Update` and `Excluded` policy ownership for Stack Chains. The Containers page displays this effective stack policy and removes individual member policy editing while a stack is chain-managed.
- Blocked direct individual updates for Stack Chain members in both UI and backend so an ordered stack update cannot be bypassed. Per-container verification, release source, snooze, history and rollback remain available; checks remain available unless the stack policy is Excluded.
- Integrated stack policy ownership into existing scheduled policy runs without adding another scheduler: Auto Stack Chains execute in configured order through their bound Automation, while bound Manual/Excluded stacks can refresh update information/read-only registry state. Normal policy scans never fall back to unordered Auto updates for chain-owned stack members.
- Preserved existing V0.8.4/V0.8.4.1 free-form chains as `Custom Chain` definitions with inherited legacy per-container policy behavior.
- Added additive SQLite migration columns `scope_type`, `scope_key` and `policy_mode` on `update_chains`, including legacy migration/regression coverage.

## 0.8.4.1

- Moved low-level Job History out of History into a dedicated Jobs page. History now focuses on update/rollback transactions and their Preflight/Verification/dependency results.
- Added atomic queued-job cancellation. Only jobs still in `queued` can be cancelled; running Docker operations remain intentionally non-interruptible for transaction safety. Queue/goroutine consumers atomically claim work so a cancelled request cannot later be resurrected.
- Added Cancel controls for queued jobs on the Jobs page and directly in the Containers per-row progress panel for queued manual checks, updates and rollbacks. Durable queued jobs are mirrored back into the Containers row after a page refresh.
- Added `cancelled` as a terminal job state for job-status polling and Update Chain waiting. Cancelling a queued chain master marks the run cancelled and releases reservations; a cancelled child update stops/fails the step under the existing chain safety rules.
- Expanded the V0.8.4 per-container progress stream with granular Preflight stages for host/dependency inspection, health/verification configuration, registry/architecture, storage, volumes/bind mounts/recovery, Config Snapshot, Restore Point and the final Preflight decision before the existing update/health/verification stages.
- Added regression coverage for atomic queued-job cancellation/claiming and cancelled terminal progress.
- No SQLite schema migration; existing V0.8.4 data remains compatible.

## 0.8.4

- Added one shared update safety pipeline used by manual updates, Auto Update policies and Update Chains: preflight, recovery preparation, Watchtower update, Docker Health/runtime verification, custom application verification and the existing rollback path.
- Added reusable Update Preflight with Green/Warning/Blocked checks for registry manifest availability, target architecture, current container state, recovery readiness, network-namespace dependencies, Docker Healthcheck/custom verification coverage, named volumes, bind mounts, disk-space visibility and major-version metadata. Red conditions block both manual and automatic updates; manual preview uses the same engine without creating recovery artifacts.
- Added container- and Compose-stack Custom Verification profiles with HTTP/HTTPS/TCP checks, expected HTTP status/content, start delay, retry count/interval and multiple combined checks. Verification state is stored as `Verified`, `Failed`, `Not configured` or pending/running, recorded in Update History/Audit and can trigger existing notifications.
- Routed failed post-update custom verification through the existing automatic Restore Point rollback implementation; full rollback now also performs post-rollback custom verification after Docker Health and reports a degraded/failed rollback if functional verification still fails.
- Added **Update Chains / Service Groups** with explicit per-host ordering, per-step wait, stop-on-failure, optional reverse rollback of previously successful members, manual Run Now, optional binding to an existing Automation policy run, chain/run history and policy enforcement. Each step uses the normal container check/update job and does not bypass the central update engine.
- Added chain-member reservation so a running chain cannot race manual checks/updates or the normal Auto Update scanner. Critical preflight/verification/rollback/dependency failures always stop a chain even when ordinary step failures are configured to continue. Interrupted chain runs are marked failed on controller restart rather than automatically resumed.
- Extended Update History with durable preflight/verification fields and added additive SQLite tables for verification profiles/state plus chain definitions, ordered steps, runs and run-step history. Existing V0.8.3.2 installations migrate in place with no mandatory user reconfiguration.
- Added Containers UI for preflight preview, per-container verification state and verification-profile editing, plus a manager-only Update Chains page with robust up/down ordering controls, Run Now and chain history.
- Extended support bundles with chain/run data and sanitized verification profile/state metadata without exposing configured URLs or expected response content.
- Added regression tests for HTTP/TCP verification, verification validation, v0.8.3.2 schema migration/preservation, verification persistence, chain persistence/order validation and interrupted-chain recovery.
- Extended legacy Docker-event database corruption recovery to preserve the new V0.8.4 verification/chain tables and copy only schema columns common to the damaged source and current destination, keeping pre-V0.8.4 databases recoverable before additive migrations run.

## 0.8.3.2

- Made Container Config discovery remote/VPN consistent: per-host API reads use endpoint-aware deadlines, the browser merges hosts incrementally, cached/live rows are preserved when one host fails, and archived snapshots are loaded independently.
- Increased pre-update recovery snapshot timeouts to 3 minutes for remote Docker endpoints and manual config snapshots to 4 minutes; dependent recovery snapshots use the same remote-aware policy.
- Batched Network Namespace candidate container inspection before updates, reducing Docker CLI round-trips across higher-latency links while retaining fail-closed safety.
- Added generous host-aware total rollback deadlines and bounded safety recovery/image cleanup contexts so broken VPN/TCP links cannot leave rollback Docker operations unbounded.
- Changed Application log presentation to newest-first and explicitly keeps Audit newest-first. Audit persistence is now bounded to the latest 5,000 entries; Docker Events and Pushover already retain 5,000 each. Application log files remain size-rotated (25 MiB plus five backups).
- Dashboard Containers summary now includes offline/non-running count.
- Restyled the Containers digest explanation as a blue Info notice, narrowed the Container Configs table to match other views, and added a GitHub sidebar link for `M9RPH/vibewatch` below Discord.
- No SQLite schema migration; V0.8.3.1 data remains compatible.

## 0.8.3.1

- Made configured Docker-host reachability asynchronous and cached. `/api/hosts` now returns immediately from the most recent probe state and schedules bounded background probes instead of synchronously waiting for every endpoint.
- Added retry-aware remote Docker probing: non-unix endpoints receive three bounded attempts; Docker CLI processes canceled by context now report `context deadline exceeded` instead of opaque `signal: killed` errors.
- Added remote-aware request budgets for container inventories, Dashboard overviews, volumes and networks while preserving the V0.8.3 stale-while-refresh browser behavior.
- Increased remote Watchtower worker readiness tolerance to four minutes and made individual `/readyz` probes bounded, preventing slow worker initialization across VPN links from being classified as failed after only 30 seconds.
- Added controller-start recovery for jobs stranded in `queued`/`running` by a previous restart/interruption, and bounded asynchronous manual check jobs so they cannot remain active indefinitely.
- Extended browser-side job monitoring for remote checks and long update/rollback operations so the UI does not time out before the backend/worker timeout budget is exhausted.
- Added context-aware exponential reconnect backoff for Docker event streams after remote TCP/VPN interruptions. Event-stream loss remains independent from Docker-host availability.
- Host UI now distinguishes `Checking Docker`/`Refreshing` from confirmed `Docker unreachable`, retains the last successful Docker-contact timestamp, and Dashboard inventory attempts are no longer suppressed solely by a transient reachability probe failure.
- Added regression tests for remote Docker ping retries, timeout error normalization and slow worker readiness.
- Fixed version propagation so Docker Compose, the runtime binary, OCI image label, web package and visible UI all report V0.8.3.1 instead of retaining the old V0.8.0 build tag/runtime constant. The Go binary version is now injected from the repository `VERSION` file during Docker builds.
- No SQLite schema migration; V0.8.3 data remains compatible.

## 0.8.3

- Hotfix: corrected the Dashboard per-host refresh state updater so `HostRefreshState.loading` is always present when an overview request fails; this fixes the TypeScript `TS2345` error during `npm run build`/Docker Compose builds.
- Reworked Container/Dashboard refresh behavior for high-latency remote/VPN Docker hosts: per-host inventory refreshes are incremental, stale-while-refresh, in-flight de-duplicated and short-term throttled instead of rebuilding all host data only after the slowest host completes.
- Manual `Check all on host` / `Check all · all hosts` now dispatches individual asynchronous container check jobs with per-container progress/stage in the existing table rows. Hosts run independently with at most two concurrent checks per host; the top bulk banner is summary-only.
- Changed Watchtower worker readiness locking from one global lock to per-host locks so a slow remote worker cannot block unrelated hosts. Scheduled host policy scans now also use bounded two-container concurrency per host.
- Batched container image metadata inspection and cached local image labels/platform data, removing the first-load image-inspect-per-container pattern on remote hosts.
- Batched Docker network inspection, avoided redundant Docker info calls, and collapsed image/build-cache `docker system df` collection into one call per overview. Dashboard overview no longer inventories images a second time solely for rollback-protection counters.
- Dashboard host overview, volume and network calls are started in parallel and each host card updates independently. Last successful Dashboard values remain available across page navigation and are labeled `Refreshing` / `Cached data` on slow or failed refreshes.
- Added a 25-second bound to container inventory requests so a broken remote endpoint cannot hold a browser request indefinitely.
- No database migration; V0.8.2.1 data remains compatible.

## 0.8.2.1

- Cosmetic consistency pass across all table-based views: table headers now align with their underlying cell content instead of inheriting the browser's centered `th` default; explicitly right-aligned action columns remain right aligned.
- Unified the remaining Dashboard inventory and History job tables with the shared Vibewatch table styling while retaining purpose-specific widths/compact density where appropriate.
- Renamed the UI section **Container Backups** to **Container Configs** and **Rollback** to **Container Rollback**. Internal `/api/container-backups` endpoints and persisted data structures remain unchanged for compatibility.
- Reordered the sidebar to **Containers → Container Configs → Container Rollback** and revised both pages' explanatory text so configuration snapshots and full writable-layer restore points are clearly separated.
- Fixed recovery-slot labeling: `Oldest` now follows the actually oldest retained snapshot instead of being tied to the third retention slot; empty retention capacity is labeled as an available/free slot.
- Dashboard host **Inventory** and **Cleanup** groups are now stacked vertically for a calmer, easier-to-scan host layout. Their internal object/action tiles remain responsive grids.
- No API, Docker update/rollback behavior or database migration changes.

## 0.8.2

- Fixed recovery Compose generation for containers that share another container's network namespace. Runtime `container:<id>` references are now resolved while the parent still exists and normalized to stable `service:<compose-service>` references when both services belong to the same reconstructed Compose project.
- For namespace parents outside the reconstructed Compose unit, snapshots use the stable Docker container name (`container:<name>`) instead of persisting the parent's ephemeral container ID. Unresolvable ID-based namespace references now fail snapshot creation closed rather than producing a misleading restore artifact.
- Recovery Compose no longer writes `hostname`/`domainname`, published ports, DNS or extra-host settings for `container:`/`service:` namespace-sharing containers, avoiding Docker's `conflicting options: hostname and the network mode` class of recreate failures.
- Docker-generated default hostnames that merely mirror a container's own runtime ID are no longer persisted for ordinary containers; explicitly configured hostnames remain preserved.
- Bumped recovery snapshot metadata schema to version 2 and added regression tests for Compose `service:` reconstruction, stable external `container:<name>` fallback and unresolved stale-ID rejection.
- No database migration is required; V0.8.1 data directories and existing retained snapshots remain readable. Newly created V0.8.2 snapshots use the corrected reconstruction semantics.

## 0.8.1

- Added generic **Docker network-namespace dependency detection** for containers whose runtime `HostConfig.NetworkMode` resolves to `container:<parent-id>` (including Compose `network_mode: service:<service>` at Engine level). Detection is performed before the update while the old parent ID still exists.
- Parent updates now capture all direct namespace dependents, preserve their prior running/stopped state and recovery configuration, verify the recreated parent first, then **recreate** each dependent against the new parent container ID. A simple restart is never used for namespace rebinding.
- Dependency handling is part of the shared update service, so it applies equally to manual and automation-driven updates. Multiple dependents are processed sequentially and dependency recreates are logged as dependency operations rather than independent image updates.
- A failed dependent recreate now fails the overall update transaction and enters the existing full Restore Point automatic-rollback path. Full rollback stops namespace dependents before restoring the parent, verifies the parent, then recreates dependents against the restored parent namespace.
- Restore Points persist the dependency relationship and retained dependent snapshot IDs. Cross-stack dependency snapshots are pinned while their parent Restore Point is retained, preventing another backup unit's retention cycle from silently degrading the transaction; they are removed when the parent Restore Point expires.
- Added structured audit events `dependency.detected`, `dependency.recreate.started`, `dependency.recreate.success` and `dependency.recreate.failed`, plus dependency count/status/details in Update History and dependency protection details on the Rollback page.
- Dependency recreation validates the captured runtime configuration before allowing the parent update, preserves stopped dependents as stopped, and refreshes Config Drift baselines after successful recreation.
- Added additive SQLite fields for dependency transaction metadata; existing V0.8.0 data directories remain compatible.

## 0.8.0

- Added retention-managed **full container restore points** for non-Swarm updates. Vibewatch now pairs the existing pre-update runtime/config snapshot with a paused `docker commit` capture of the target container writable layer.
- Added a dedicated **Rollback** page with per-restore-point host/container/version/image metadata, protection status, mounted-volume/bind warnings, restore history/status and one-click full rollback.
- Added a direct **Rollback** action to the Containers table and linked V0.8.0 Update History rows to their full restore point. Pre-V0.8.0 history keeps the legacy image/config rollback path.
- Added conservative **automatic failed-update rollback** when the replaced target is missing, stopped, restarting or explicitly unhealthy. Containers without a Docker healthcheck receive a short running-state stability observation without application-specific health guessing.
- Added a temporary **pre-rollback safety image** during full restore so a failed destructive restore can best-effort reconstruct the state that existed immediately before rollback.
- After successful manual/automatic rollback Vibewatch now **snoozes the currently available remote digest**, preventing Auto Update from immediately reinstalling the failed image; a newer digest clears the snooze through the existing digest-bound logic.
- Restore images are protected from Vibewatch image cleanup while retained. When the linked recovery snapshot expires, the restore point is marked expired and its internal image tag is removed. Existing image/volume/network rollback protection remains retention-aware.
- Added explicit UI/documentation that **Docker volume and bind-mount contents are not included** in full-container restore images. Swarm remains configuration-only for one-click rollback.
- Added additive SQLite `restore_points` persistence plus `update_history.restore_point_id`, migration/round-trip tests, and selection logic that prefers the newest usable full restore point over a newer degraded record.
- Narrowed the worker-operation lock to the actual Watchtower update call, avoiding nested read-lock acquisition during post-update checks/automatic rollback while still keeping worker maintenance mutually exclusive with active Watchtower operations.

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