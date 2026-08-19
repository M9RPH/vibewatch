# Changelog

All notable public changes to Vibewatch are documented here.

## 1.0.17

- Hardened the built-in Developer Update path against disk exhaustion and interrupted rollback.
- Added workspace/data/Docker-root capacity preflight with a conservative 4 GiB Docker build-space floor and inode checks.
- Added a live Docker-root disk-pressure guard that cancels builds before the recovery reserve is exhausted.
- Reworked source apply/rollback to verified atomic per-file replacement so ENOSPC cannot leave a half-restored source tree such as a workspace missing `go.mod`.
- Added emergency status-write reserve, orphaned helper/state reconciliation, `recovery_required`, Safe Cancel, and Retry Recovery for Developer Updates.
- Removed the four-hour stale-active bypass; unresolved Developer Updates remain blocked until reconciliation proves a safe terminal state.
- Pinned the pre-update controller image and changed post-switch rollback to restore it with `--no-build`, avoiding a second build during recovery.
- Preserved active/recovery-required update artifacts and pinned rollback images until recovery is resolved.
- Added strict source-tree readiness validation and Developer Update regression tests for the observed disk-full/missing-`go.mod` failure.

## 1.0.16

### Transaction & Chain Integrity Consolidation

- Failed automatic rollback no longer finalizes an update as an ordinary terminal failure. The transaction remains `recovery_required`, blocks further mutation of the target and keeps its recovery baseline protected until reconciliation succeeds.
- Recovery/retention now pins last-known-good restore points for active recovery contracts and for legacy rollback failures that older builds incorrectly recorded as terminal `failed`. Graph-aware Recovery GC is the only path that may delete restore-point metadata/artifacts; opportunistic database pruning was removed.
- Standalone updates, restore-point rollback, legacy history rollback, chain `Restart/Recreate if current`, update-transaction crash recovery and chain crash recovery now share one re-entrant mutation scheduler: up to three different hosts may mutate concurrently, with at most one mutation pipeline per host.
- Chain Safe Cancel now supports running chain jobs. Cancellation is cooperative: an already-started atomic step is settled/recovered, no subsequent step is started, and orphaned chain executors enter transaction-safe reconciliation before becoming cancelled.
- Incomplete rollback of completed chain members leaves the chain run `recovery_required` instead of terminally failed; stale chain-owned leases are reclaimed before recovery.
- Runtime-fidelity verification now covers the broader Docker runtime contract preserved by deterministic recreation, including resource limits, namespace modes, tmpfs, groups/links/volumes-from, DNS/hosts, log configuration, ulimits, device requests, OOM settings and other HostConfig fields.
- Added regression coverage for scheduler host/global limits and re-entrancy, rollback-required mutation blocking, legacy recovery pinning/pruning, chain Safe Cancel settlement and expanded runtime-contract drift detection.

## 1.0.15

### Fixed

- Restore-derived runtimes (`io.vibewatch.restore-*`) no longer delegate container recreation to Watchtower. Vibewatch applies the already-resolved immutable transaction target directly from the preserved runtime delta, preventing loss of explicit environment variables and Compose metadata after rollback-derived updates.
- Added a pre-mutation runtime fidelity contract and post-mutation verification for explicit environment variables, Compose/user labels, command/entrypoint overrides and critical HostConfig. Worker-created drift is repaired once by deterministic exact-target recreation before Docker health checks continue.
- Restore provenance labels are no longer propagated onto a successfully updated forward target; Compose labels remain preserved.
- Automatic/manual protected-data rollback can now fall back to the retained restore manifest when a broken target container has lost its Compose identity, allowing recovery from the exact failure mode where the failed recreate dropped stack labels.
- Added SABnzbdVPN/WireGuard regression coverage for PUID/PGID/VPN environment preservation, Compose-label fidelity, secret-safe mismatch reporting and retained-manifest rollback recovery.

## 1.0.14

### Fixed

- Fixed rollback-created Vibewatch restore images leaking into mutable application tags such as `:latest`. Restore recreation now preserves the pre-rollback mutable tag target and removes legacy restore-contaminated aliases instead of retagging a restore commit as the application image.
- The immutable registry/config target is now materialized locally and the mutable application ref is aligned to that exact transaction target before Watchtower is allowed to mutate a container.
- Post-worker image verification still fails closed, but any observed wrong-image mismatch can now be corrected by deterministic exact-target recreation. This covers workers switching from one old restore image to another instead of applying the registry target.
- Added regression coverage matching the observed SABnzbdVPN `68fe… -> 44fe…` restore-tag loop while the authoritative target remained `610c…`.

## 1.0.13

### Fixed

- Fixed deterministic target recreation misclassifying Vibewatch's own legacy `docker create --entrypoint` normalization as explicit user `Cmd`/`Entrypoint` overrides. Multi-element image entrypoints that had been round-tripped through rollback no longer pin stale startup paths onto a newer image.
- Command-only overrides are recovered correctly even after that legacy entrypoint normalization, while genuine explicit entrypoint overrides continue to be preserved.
- Added a regression case matching the observed SABnzbdVPN state where `/usr/bin/dumb-init --` plus `/bin/bash /usr/local/bin/init.sh` had been normalized by an earlier rollback and then incorrectly replayed onto the new image.

## 1.0.12

### Legacy restore lineage compatibility

- Fixed deterministic target apply failing for containers still running from older Vibewatch `docker commit` restore images after the referenced restore-point metadata aged out through retention.
- Added fail-closed restore lineage resolution: embedded restore provenance → retained restore-point metadata → validated Docker Parent / unique deepest local RootFS-layer ancestry.
- Docker Parent metadata is never trusted blindly; when present it must match the restore image layer ancestry, and layer-inventory fallback requires one unambiguous deepest candidate.
- New restore images embed `io.vibewatch.restore-original-image-id` and `io.vibewatch.restore-original-image-ref`, decoupling future forward updates from backup/restore metadata retention.
- Added regression coverage for embedded provenance, expired legacy restore metadata, config-only commit Parent recovery and ambiguous ancestry rejection.
- Failed update history now records the immutable target digest on pre-recreate failures instead of the currently running restore-image digest.

## 1.0.11

### Deterministic recreate fidelity and rollback diagnostics

- Fixed deterministic target recreation carrying fully materialized defaults from the **old** image (`Cmd`, `Entrypoint`, inherited environment and image labels) into the **new** image. Forward recreation now computes only real runtime/user overrides against the original source image defaults so the target image can use its own startup contract.
- Restore-image provenance is followed recursively back to the original immutable base image before override deltas are calculated. This keeps VPN/application settings as runtime overrides even after repeated restore/rollback cycles.
- Added post-recreate runtime-fidelity verification for critical host configuration including privileged mode, capabilities, sysctls, published ports, mounts, networks, DNS settings, devices, restart policy, security options and runtime.
- Automatic restore keeps the committed restore image pinned to the restored container while best-effort retagging the original immutable base image back to the original mutable reference, preventing restore commits from poisoning a later `:latest`/untagged Watchtower decision.
- Failed update history now preserves the immutable image ID that was actually attempted even when automatic rollback changes the live image before history is finalized.
- Support bundles now include bounded per-job stage logs, and Custom Verification failures record their configured readiness budget plus a safe pre-rollback runtime snapshot without environment values or container secrets.
- Added regression coverage for SABnzbdVPN/WireGuard-style runtime settings, inherited versus explicit command/entrypoint behavior, capability normalization, VPN-critical fidelity checks and restore-image lineage.

## 1.0.10

- Fixed cold Data Protection updates failing immediately after a successful restore-point capture with `protected service did not recover after cold snapshot: context canceled`.
- Continuity lock ownership is now transferred as lock metadata rather than by exporting the short-lived restore-point timeout context.
- Rebinds shared/exclusive continuity markers to the durable update-job context, preserving safe cancellation and cross-host DNS/control-plane protection.
- Added regression coverage for capture-context cancellation and nested registry access while a transferred continuity guard remains held.

## 1.0.9

### Cold snapshot recovery and safe running-job cancellation

- Fixed a cold Data Protection recovery race where a just-restarted container in transient `created`/`restarting` state was treated as permanently failed on the first Docker inspect.
- Runtime recovery now uses a bounded startup grace window and, for containers without a Docker healthcheck, requires a short continuously-running stability window before application verification begins.
- Custom Verification still remains the application-level authority after Docker runtime recovery; configured start delay/retry behavior is no longer bypassed by an immediate transient runtime failure.
- Added cooperative **Safe cancel** for running container-update jobs. Pre-mutation cancellation stops cleanly; once an atomic Docker mutation begins, Vibewatch completes required recovery/verification instead of killing Docker work halfway through.
- Added persistent `cancel_requested` state, idempotent cancellation API handling, live update controls and Jobs-page controls.
- Hardened job finalization with fresh non-cancelled contexts, bounded retries and a deferred transaction/job reconciliation guard so a completed goroutine cannot remain as a ghost `running` job.
- Startup recovery now reconciles stale `running`/`cancel_requested` jobs whose update transaction is already terminal, while orphaned post-mutation transactions enter the existing recovery path instead of being falsely marked cancelled.
- Transaction-recovery job finalization now uses the same durable settlement path.
- Added deterministic regression coverage for transient restart recovery, bounded startup failure, running-job cancellation and terminal-transaction reconciliation.

## 1.0.8

### Multi-host control-plane continuity

- Added a global reader/writer continuity barrier: ordinary checks and ordinary lifecycle mutations remain parallel, while DNS-capable mutations become exclusive.
- When a DNS-capable mutation is pending, existing shared checks/ordinary mutations finish first; new shared work waits until the DNS service has passed Docker, Custom Verification and DNS continuity proof.
- Policy automation scans up to three Docker hosts concurrently, and the update queue dispatches up to three pipelines across different hosts while keeping one pipeline per host. Ordinary lifecycle windows can overlap across hosts; DNS/control-plane windows are exclusive.
- DNS-capable containers are detected generically from port 53 runtime configuration and must pass an uncached cache-busting resolver probe plus the real registry-host lookup before queued network work resumes.
- Cold Data Protection now keeps the continuity barrier across writer restart and recovery, closing the gap where another host could begin registry work before an infrastructure service was truly usable.
- Reordered cold-capture recovery so runtime/application readiness is proven before the first post-capture registry lookup.
- Update Chain restart/recreate actions and restore-point rollbacks use the same shared/exclusive continuity policy.
- Added concurrency and DNS-control-plane regression tests; full Go tests and race tests pass.

## 1.0.7

### Deterministic update pipeline

- Fixed a five-minute Docker image-platform cache bug where mutable references such as `:latest` could continue returning the pre-pull image ID immediately after a successful pull.
- Deterministic target preparation now proves the exact immutable platform image/config ID after pull instead of trusting the mutable tag identity.
- Single-container update checks now use Vibewatch's own platform-specific registry comparison as the authoritative discovery path; Watchtower remains the update execution worker.
- Fixed the post-rollback state where the newer image remained locally tagged while the running container used the restored older image, causing Watchtower checks to incorrectly report `update_available=false`.
- Update detection now stores comparable Docker/OCI config digests for current and latest identities, improving snooze, rollback and transaction target consistency.
- Added regression coverage for the AdGuard Home arm64 rollback/no-op sequence and mutable-tag cache invalidation.

## 1.0.6

### Snooze recovery

- Fixed rollback-created snoozes becoming effectively impossible to release when cached current/latest image identity temporarily collapsed after a failed update/rollback cycle.
- Unsnooze now preserves the explicit snoozed digest as the best known remote target when needed, immediately making the update actionable again without changing policy ownership.
- Added direct **Unsnooze update** handling in the compact Container Inspector and made the existing details action independent of stale latest/current cache equality.
- Added **Unsnooze stack** for stack members and **Unsnooze all** for Update Chain members.
- Added an audited chain-level unsnooze API; releasing a snooze never changes Manual/Auto/Excluded policy or Chain configuration.
- Includes the v1.0.4 platform-specific target-image identity fix for standalone and Chain updates.

## 1.0.4

### Multi-architecture target verification hotfix

- Fixed a v1.0.3 regression where post-update verification could compare a registry **manifest digest** with Docker's running **image/config digest**. Those are different OCI identities and caused correctly updated multi-architecture images to be falsely rejected and automatically rolled back.
- Persist the platform-specific target image/config ID separately from the update-detection digest in update transactions.
- Update execution now resolves the exact platform config digest before image mutation and blocks safely if that identity cannot be established.
- Crash recovery uses the persisted target image ID; older transactions without the new field retain the compatibility recovery path.
- Update Chains inherit the corrected verification automatically through the shared container update transaction pipeline.
- Added the AdGuard Home arm64 regression case discovered from the v1.0.3 support bundle.

## 1.0.3

### Update correctness

- Added mandatory post-update target-image verification. A worker response with `failed=0` is no longer sufficient to declare success when the requested image was not actually applied.
- Fixed false-success updates where Watchtower returned `updated=0 / skipped=1` and the running container remained on the old image.
- Update History now records the real resulting image/version after failed application instead of presenting the attempted target as applied.
- Avoid unnecessary automatic rollback when image verification proves that the target was never applied and the original image is still running.

### Data Protection and infrastructure services

- Removed the deferred target restart optimization after cold Data Protection capture. Previously running data writers are restored before registry/update activity.
- Added a runtime-stability gate after a cold-captured update target is restarted, preventing infrastructure services such as DNS from remaining unavailable while the update worker needs network/registry access.

### Update Chains and recovery

- Chain member updates inherit the same target-image verification through the shared update transaction path.
- A Chain member whose target image was not applied is treated as a critical failed member; shared-data rollback logic no longer treats an explicitly `target_not_applied` transaction as a mutated failed step.
- Controller restart recovery verifies the expected target image before accepting the live runtime as a successfully recovered update.

## 1.0.2

### Update Chains live transaction UI

- Rebuilt **Run now → Chain Preflight** with the Web UI v2 live transaction design used by SSH Quick Setup and container updates.
- Chain Preflight now exposes real backend progress per member instead of a static waiting dialog.
- Advisory warnings and blockers stay attached to the exact chain member that produced them.
- After approval, the Chain execution window follows every configured member live through checking, updating, restart/recreate, verification, failure and recovery states.
- Preflight warnings remain visible during Chain execution so the operator keeps the original safety context.

## 1.0.1

### Fixed

- Fixed a Web UI v2 interaction bug in the Update Chain editor where the portaled Policy dropdown could be visible above the dialog while clicks were handled by a switch underneath.
- Policy menus now portal into the active dialog content when opened from a modal, retain fixed viewport positioning to avoid clipping, and explicitly own pointer interaction.
- Containers-table policy dropdown behavior remains unchanged through the normal body-portal fallback.


## 1.0.0

### Release milestone

- Promoted Vibewatch to **v1.0.0** as the first stable public release.
- Updated release packaging, versioned install files and GitHub-facing documentation for the stable release line.

### Web UI v2

- Finalized the new Web UI v2 across Dashboard, Hosts, Containers, Automation, Jobs, History, Logs, Users, Settings, Update Chains and Recovery views.
- Added the new Vibewatch lighthouse/whale branding and refreshed public screenshots.
- Standardized banners, icon mini-cards, typography, spacing and inspector styling across the application.

### Live operation inspectors

- Preflight now uses the same staged live transaction window as SSH Quick Setup.
- Manual container updates and rollbacks now use the same live staged transaction inspector, preserving the exact active/failed stage.
- SSH Quick Setup now exposes visible live progress so remote host setup is no longer a background black box.

### Connectivity and operations

- Hardened SSH Quick Setup for legacy TCP and managed mTLS with richer Docker diagnostics, transaction-aware rollback and safer handling of existing Docker daemon configuration.
- Added persistent notification read-state with mark-as-read actions and bell counts based on unread items only.
- Added Owner-only Developer update/upload flow to apply development ZIP builds directly from the UI.
- Added service-icon resolution for the container inspector with fallback handling when no dedicated icon exists.

### Documentation and release preparation

- Reduced the public repository landing page to a short description, name origin and installation path.
- Updated release notes, architecture documentation and operational docs to reflect the stable 1.0 release.
- Replaced outdated screenshot assets with the current Web UI v2 screenshots.
- Updated CI release sanity checks to read the version dynamically from `VERSION`.


## 0.9.5

### Distribution

- Added an official release Compose file (`compose.yml`) for source-free installations.
- Release tags now publish `linux/amd64` and `linux/arm64` images to `ghcr.io/m9rph/vibewatch`.
- Container publishing only occurs for a version-matching `vX.Y.Z` tag after release validation and Docker integration tests pass.
- Added OCI source/license metadata so the GHCR package can be associated cleanly with the Vibewatch repository.


- Fixed Git release packaging so runtime data skeleton `.gitkeep` files under `data/backups/` and `data/logs/` are tracked correctly instead of being shadowed by broad ignore rules.

### Recovery and crash safety

- Added Chain-level crash recovery. Running Chain jobs are no longer blindly failed during controller startup; child update transactions are reconciled first and the Chain is reconstructed from recorded step/job state.
- Added Chain recovery states: `Recovered`, `Interrupted` and `Recovery required`.
- Started Chain restart/recreate actions are reconciled after a controller restart. Recreate actions use their retained restore point when runtime verification cannot confirm a good state.
- Remaining Chain steps are never automatically resumed after a controller restart. This avoids continuing an unattended multi-service mutation from an uncertain midpoint.
- New Chain runs and container updates are blocked while the same target has unresolved recovery-required state.
- A successful explicit rollback resolves a recovery-required update transaction tied to that restore point.
- Active/recovery-required transactions and Chain runs now protect referenced restore points from recovery cleanup.
- Recovery GC also removes orphaned short-lived Vibewatch helper containers on idle hosts.
- Added **Cleanup unusable** on Container Rollback for expired/degraded recovery artifacts. Ready restore points and recovery-referenced restore points are never removed by this action.

### Release/documentation

- Reworked README and architecture documentation for a public GitHub release.
- Added dedicated documentation for installation, update pipeline, verification, Data Protection/rollback, Update Chains, automation and security.
- Internal development/context notes are no longer part of the public repository package.

### Release preparation hotfix

- Renamed the canonical environment-variable prefix from legacy `WTUI_*` to `VIBEWATCH_*`. Existing `WTUI_*` values remain accepted as fallbacks, so current installations do not need an immediate `.env` migration.
- Rebranded the Go module path to `github.com/m9rph/vibewatch` so CI/build output no longer exposes the pre-Vibewatch project name.
- Fixed SQLite JSON hydration for restore-point dependency metadata and Data Protection manifests. The raw JSON remains hidden from public API serialization. This fixes `TestDependencyMetadataRoundTrip` in GitHub Actions and ensures persisted recovery metadata is available after DB reads.
- Added the public explanation of the Vibewatch name and its AI-assisted development workflow.
- Internal engineering/context notes remain in `.vibewatch-internal/`, which is excluded from Git and Docker build contexts.

### Host connection hardening

- Hardened SSH Quick Setup for both legacy TCP and managed mTLS with Docker configuration preflight, port/listener checks, preserved local Unix-socket access, full dockerd restart diagnostics and transactional rollback until the controller verifies the new endpoint.
- Secure in-place host upgrades now test newly generated mTLS client credentials before replacing persisted controller credentials, and managed credential commits are atomic.

### Transaction UI

- Manual container updates and rollbacks now use the same live staged transaction inspector as SSH Quick Setup and Preflight, driven by real job progress and preserving the exact failed stage for diagnosis.

### Compatibility

- Existing databases migrate automatically with additive Update Chain recovery metadata and Recovery GC counters.
- Existing policies, automations, restore points and Chain definitions remain compatible.
- Fixed the v1.0.0 source-build compatibility issue caused by an ES2021-only `String.replaceAll()` call while the frontend target remains ES2020, and escaped the legacy runtime-migration shell variable so Docker Compose no longer emits the `c variable is not set` warning.

## 0.9.4

- Added optional Data Protection for explicitly selected bind mounts and Docker volumes, available at standalone-service or Compose-stack scope.
- Added host-local data restore archives, local/external/unknown storage classification, mount size planning and restore-storage capacity checks.
- Added data-aware restore points and rollback, including transaction-scoped persistent-data baselines for Update Chains.
- Added compact Data Protection status to Containers and Data Protection checks to Preflight.
- Expanded Container Rollback with restore coverage, protected mount details and Chain transaction context.
- Improved rollback snooze UX after failed updates.
- Added scheduled cleanup automations and a hard restore-storage Preflight blocker.

## 0.9.3

- Made unattended updates conservative by default: automatic updates require a clean Preflight unless advisory warnings are explicitly allowed.
- Added Chain plan Preflight before the first automatic Chain mutation.
- Added `Held` visibility and Needs Attention integration for updates blocked by unattended Preflight safety.
- Added manual Chain Preflight review before `Run Now`.

## 0.9.2

- Added Docker TLS/mTLS host connectivity and credential rotation.
- Added CI/container publishing workflows and official GHCR release installation layout.
- Added Owner application backup bundles and exportable operational history.
- Added `Why didn't this update?` diagnostics and major UI consistency work.

## 0.9.0 - 0.9.1

- Introduced persisted update transaction stages, operation leases, restore integrity validation and Recovery GC foundations.
- Added reliability improvements for slow/remote Docker hosts and host cleanup progress.

## 0.8.x

- Added full writable-layer restore points, automatic rollback, Custom Verification, Preflight, Update Chains and network-namespace dependency handling.
- Added Config Drift, configuration snapshots and update history improvements.

Earlier development releases established multi-host management, policies, roles, Pushover notifications, container inventory and the Vibewatch UI foundation.
