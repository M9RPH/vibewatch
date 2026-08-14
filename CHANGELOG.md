# Changelog

All notable public changes to Vibewatch are documented here.

## 0.9.5

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

### Compatibility

- Existing databases migrate automatically with additive Update Chain recovery metadata and Recovery GC counters.
- Existing policies, automations, restore points and Chain definitions remain compatible.
- Fixed the v0.9.5 source-build compatibility issue caused by an ES2021-only `String.replaceAll()` call while the frontend target remains ES2020, and escaped the legacy runtime-migration shell variable so Docker Compose no longer emits the `c variable is not set` warning.

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
