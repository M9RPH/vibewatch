# Data Protection and rollback

Data Protection extends a normal pre-update restore point with explicitly selected persistent Docker data.

It is designed for **update rollback**, not long-term backup retention.

## Restore point contents

A full non-Swarm restore point can include:

- runtime/config snapshot;
- pre-update image information;
- container writable layer captured with a paused Docker commit;
- selected bind mounts and/or Docker volumes;
- network-namespace dependency recovery metadata.

## Selecting data

Vibewatch never automatically selects every mount. The operator chooses only data needed to restore the application after a bad update.

Examples:

- Plex: protect `/config`, not a multi-terabyte movie library.
- Immich: protect rollback-critical database state while deciding separately whether large media storage belongs in an update restore point.

Profiles can be scoped to a standalone service or a Compose stack.

## Cold capture

For selected local writable data, Vibewatch identifies Docker containers writing to the same selected mount and stops the required writers for a consistent file-level capture.

The actual update target is not unnecessarily restarted with the old image after capture when it is about to be replaced. Non-update writers are restarted as required.

Update Chains retain their configured restart/recreate behavior; Data Protection does not create a second application-dependency engine.

## Host-local storage

Archives are stored in a Vibewatch-managed Docker volume on the target Docker host. Data is not streamed back to the controller by default.

The Dashboard and Data Protection dialog expose cached restore-storage capacity. Local mount sizes can be measured through the helper; external/network mounts are not recursively scanned automatically.

## Network mounts

For bind mounts, Vibewatch checks the host source filesystem. SMB/CIFS/NFS and known network/FUSE storage is classified External. Unknown filesystem types remain Unknown instead of being assumed Local.

External mounts may be explicitly selected, but Vibewatch cannot stop writers located outside the managed Docker host. Therefore external storage cannot receive the same consistency guarantee as a fully controlled local mount.

## Retention

Restore-point retention applies to the whole recovery unit. When a restore point expires, its retained config/data/image recovery artifacts are released together.

## Rollback

A data-aware rollback:

1. validates restore-point integrity;
2. creates a temporary safety state where possible;
3. stops required data writers;
4. restores selected persistent data;
5. restores runtime/image/config state;
6. recreates required network-namespace dependents;
7. starts required writers/services;
8. runs Docker health/running-state validation and Custom Verification.

A successful rollback snoozes the failed update digest so an unattended policy run does not immediately install the same failed image again.

## Recovery GC

**Run recovery GC** validates retained restore points and removes orphaned Vibewatch-owned recovery objects.

**Cleanup unusable** permanently removes eligible expired/degraded recovery artifacts. It does not remove Ready restore points and does not remove a restore point referenced by an active or recovery-required update/Chain transaction.
