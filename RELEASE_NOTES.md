# Vibewatch v1.0.17

v1.0.17 hardens the built-in Developer Update path after a real ENOSPC failure showed that a Docker build could exhaust the host filesystem and leave the mounted source tree only partially restored. This release intentionally does not change the normal container or Update Chain engine.

## Developer Update hardening

- Adds a capacity preflight before installation. The controller verifies workspace/data headroom and probes the Docker root filesystem; builds are blocked when Docker has less than 4 GiB free or recovery inode/headroom requirements are not met.
- Adds a live disk-pressure guard during the build. If Docker free space falls below the 768 MiB recovery reserve, the build is cancelled before the filesystem reaches 100%.
- Makes source apply and rollback failure-safe: files are copied to sibling temporary files, fsynced, atomically renamed, verified against the staged source, and stale files are removed only after the replacement tree is durable.
- Validates the complete Vibewatch source contract, including `go.mod`, before a source tree is accepted as ready.
- Reserves emergency status-write space so an ENOSPC condition has room to persist a terminal/recovery state.
- Removes the previous four-hour stale-active bypass. Active Developer Update states now remain blocked until they are explicitly reconciled.
- Reconciles orphaned Developer Update states at controller startup. If the helper is gone and the previous source can be proven, Vibewatch repairs the workspace and terminalizes the interrupted run; ambiguous post-switch states become `recovery_required`.
- Adds **Safe cancel** for pre-switch Developer Update stages. A build is cooperatively cancelled and the previous source is restored/verified before the run becomes terminal.
- Adds **Retry recovery** for `recovery_required` Developer Updates.
- Cleans orphaned `vibewatch-dev-updater-*` helper containers without touching an active helper.
- Pins the currently running controller image before the build. If post-switch verification fails, rollback restores source/database and recreates the previous controller from that pinned image with `--no-build`, avoiding a second build/disk spike during recovery.
- Keeps the pinned rollback image while a Developer Update remains `recovery_required` so manual/startup recovery still has a known-good controller image.
- Preserves active/recovery-required Developer Update artifacts during cleanup.

## Validation

- Full Go test suite.
- Race-detector coverage for the Developer Update, app and Docker helper paths.
- `go vet`.
- Server and Developer Updater builds.
- Version consistency check across release/runtime/install files.
- Developer Update ZIP staging validation with the integrated `StageArchive()` implementation.
