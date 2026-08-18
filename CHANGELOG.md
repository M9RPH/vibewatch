# Changelog

All notable public changes to Vibewatch are documented here.

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
