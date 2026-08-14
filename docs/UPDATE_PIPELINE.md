# Update pipeline and Preflight

Vibewatch separates update discovery from destructive execution. An available image does not automatically mean a container is safe to replace.

## Manual update

```text
Update requested
 -> Preflight review
 -> Restore Point
 -> Update
 -> Docker health/running-state check
 -> Custom Verification (if configured)
 -> metadata refresh
 -> success or rollback
```

The Preflight window opens before the mutation and reports checks as they complete.

## Preflight levels

- **Safe / green:** passed.
- **Info / blue:** informational; does not make Preflight dirty.
- **Warning / yellow:** operator decision/advisory condition.
- **Blocked / red:** update cannot proceed.

If a Custom Verification profile exists, absence of a Docker healthcheck is informational rather than a warning. Vibewatch still observes Docker running-state stability before application verification.

## Automatic updates

Automatic updates require a clean Preflight by default. A target can explicitly allow advisory warnings, but hard blockers remain blocking.

When an automatic update is held, the update is recorded as skipped/held rather than as a false update failure. The container appears under **Needs attention** while the relevant update remains available.

## Restore storage gate

For full restore points, Vibewatch checks host-local restore capacity before capture. The estimate includes known writable-layer/local protected-data sizes plus a safety reserve. If available restore storage is insufficient, Preflight is blocked before any destructive change.

External/network mount sizes are not automatically scanned and cannot be included reliably in that estimate.

## Controller restart

Update stages are persisted. On startup, Vibewatch reconciles interrupted transactions against the actual Docker runtime. A healthy and successfully verified updated container can be retained; otherwise Vibewatch can roll back to a valid restore point. Unresolved state is marked **Recovery required** and blocks another update of the target.
