# Recovery and crash safety

Vibewatch persists update progress so a controller restart does not erase the context of a destructive operation.

## Single-container updates

An update transaction records its current stage and linked restore point. After Vibewatch starts again, an unfinished transaction is reconciled against the real Docker runtime:

1. Vibewatch inspects the current container/runtime.
2. Docker running/health checks are evaluated.
3. Custom Verification is evaluated where configured.
4. If the updated runtime is valid, it may be retained.
5. If it is not valid and the retained restore point is usable, Vibewatch attempts rollback.
6. If neither outcome can be established safely, the transaction becomes **Recovery required**.

A target in **Recovery required** is held from another update until the recovery state is resolved. A successful explicit rollback of the linked restore point resolves that hold.

## Update Chains

A controller restart does not blindly resume the remainder of an Update Chain.

Vibewatch first reconciles child container update transactions. It then evaluates any Chain step that had already started. Restart/recreate steps are verified against the live runtime and may use their retained restore point where required.

Remaining steps that never started are deliberately not executed automatically after the restart. The Chain ends as one of:

- **Interrupted** — execution stopped safely and no unresolved mutation remains.
- **Recovered** — started work was reconciled or restored successfully.
- **Recovery required** — operator action is still required before another run is allowed.

When a Chain uses a shared protected-data baseline, recovery keeps application software and restored data on a compatible transaction baseline rather than leaving old data with partially newer software.

## Recovery GC

**Run recovery GC** is an integrity/reconciliation operation. It validates retained restore points, enforces retention and removes orphaned Vibewatch-owned restore/helper objects when safe.

It does not blindly delete degraded recovery state.

## Cleanup unusable

**Cleanup unusable** is the explicit permanent cleanup action for expired/degraded/failed restore-point remnants.

It never removes:

- a Ready restore point;
- a restore point referenced by an active update transaction;
- a restore point referenced by a recovery-required update/Chain transaction.

Use this after reviewing recovery state when unusable artifacts are no longer needed.

## Independent backups remain necessary

Crash recovery and Data Protection are designed to make container updates reversible. They are not a replacement for independent application/database backups or off-host disaster recovery.
