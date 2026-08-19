# Update pipeline and Preflight

Vibewatch separates update discovery from destructive execution. An available image does not automatically mean a container is safe to replace.


## Update discovery identity

Vibewatch is the authoritative source for single-container update discovery. It compares the immutable Docker image/config ID of the running container with the config digest from the registry manifest selected for that host's exact platform. Watchtower is used to execute the mutation, not to decide whether a post-rollback runtime is up to date.

This distinction matters after rollback: the newer image may remain downloaded and tagged as `:latest` while the restored container intentionally runs the previous immutable image ID. A tag-based or local-cache-based check can incorrectly call that state current. Vibewatch therefore tracks comparable immutable config identities for both current and remote target state.

Mutable image references such as `:latest` are never trusted as long-lived identity cache keys. After a pull, deterministic target application proves that the exact transaction target image/config ID exists locally before recreating the container.

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

## Post-update image verification

Beginning with v1.0.4, Vibewatch does not treat a successful update-worker HTTP response as proof that the image changed. Before Docker health or Custom Verification can pass the transaction, Vibewatch inspects the live container and confirms that its Docker image/config ID matches the platform-specific OCI config digest resolved before the update. Registry manifest digests and Docker image IDs are tracked separately and are never compared directly. A skipped worker operation with the old image still active is a failed update, not a successful verification of the old application.

If the worker reports a no-op but the transaction target differs from the running image, Vibewatch enters a deterministic fallback. It ensures the exact target image is locally available, derives only the true runtime/user overrides relative to the original source image defaults, and recreates the protected runtime from the immutable target ID. Image-owned defaults such as inherited environment, command, entrypoint and OCI labels therefore come from the target image unless explicitly overridden, while host configuration such as mounts, ports, networks, capabilities, sysctls, devices and restart policy is preserved. For Vibewatch restore images, provenance is followed back through restore points to the original immutable base image before the delta is calculated. The recreated runtime must then pass a critical runtime-fidelity comparison before image verification, Docker health and Custom Verification may succeed.

When Data Protection performs a cold capture, any writer that was running before capture is restored before the update worker is allowed to perform registry activity. The target must also reach a stable Docker running state first. This prevents an update from taking down an infrastructure dependency such as the DNS service required by the update worker itself.

## Multi-host control-plane continuity

Multi-host discovery and lifecycle execution use bounded concurrency. Registry/update discovery may run concurrently across hosts, and the queue dispatches up to three update pipelines across different hosts with at most one active pipeline per host. Ordinary update/rollback mutation windows take a shared continuity guard, so up to three host pipelines can keep moving. A DNS-capable target takes the writer/exclusive guard; a pending DNS writer lets already-running shared work finish and then prevents new registry/Watchtower checks or ordinary mutations until runtime, application and DNS readiness are proven.

After a restore-point rollback, the running container remains pinned to the committed restore image while Vibewatch best-effort restores the original immutable base image behind the original mutable tag. This prevents a restore commit from becoming the apparent `:latest` image for a later worker run.

Cold Data Protection uses the same shared/exclusive guard from writer stop through restart recovery. If the target was a running protected writer, Vibewatch proves Docker stability and Custom Verification first, then performs a fresh registry round-trip before its guard opens or update execution begins.

Containers exposing DNS port 53 receive an additional control-plane readiness proof. Vibewatch derives the registry hostname from the configured image, first queries a unique child hostname to defeat positive DNS caching (an authoritative NXDOMAIN counts as a live resolver response), and only then requires the real registry hostname to resolve. The same rule applies after update and rollback and is runtime-config driven, not tied to an AdGuard-specific name or image.

## Cold-snapshot startup recovery

A cold Data Protection capture may stop a previously running writer briefly. Restarting that container is not treated as complete merely because `docker start` returned successfully, and a single transient `created`, `restarting` or not-yet-running inspect is not treated as permanent failure.

Vibewatch waits within a bounded startup grace window for the runtime to stabilize. If the container has no Docker healthcheck it must remain continuously running for a short stability window; if it has a Docker healthcheck, the bounded Docker-health window is honored. The configured Custom Verification profile then runs normally, including its start delay and retries, before registry/update activity resumes.

## Safe cancellation of a running container update

Running container-update jobs support cooperative **Safe cancel**. The persisted job enters `cancel_requested`, but Vibewatch never sends a force-kill into an atomic Watchtower/recreate/rollback operation.

Before image mutation, a cancel request settles the transaction immediately as cancelled after any required cold-snapshot writer recovery. Once the transaction crosses into image mutation, the current atomic Docker work and required verification/recovery are allowed to finish. The final observed result wins: a successfully applied and verified update remains success even if the operator requested cancellation too late.

Job finalization is reconciled against the persistent update transaction. On controller startup, stale active job rows backed by terminal transactions are repaired automatically; non-terminal post-mutation transactions continue through crash recovery instead of being falsely cancelled.
