# Vibewatch Architecture

This document describes the public architecture of Vibewatch v0.9.5. It focuses on stable concepts and operator-visible behavior rather than internal development notes.

## Overview

Vibewatch is an agentless Docker update controller. One Vibewatch controller manages one or more Docker Engines. Docker operations are performed through each configured Docker Engine connection; no permanent Vibewatch agent is installed on remote hosts.

```text
Browser
  |
  v
Vibewatch controller
  |-- SQLite state / audit / history
  |-- Scheduler
  |-- Update / recovery engine
  |-- Registry and release metadata
  |
  +---- Docker Engine A
  |       |-- application containers
  |       |-- Vibewatch-managed Watchtower worker
  |       `-- short-lived helper containers when required
  |
  +---- Docker Engine B
          `-- ...
```

## Controller

The controller is a Go application with an embedded/static web frontend. Persistent controller state is stored under `/data`; the default Compose installation bind-mounts this to `./data` on the controller host.

SQLite stores configuration and operational state including:

- hosts and permissions;
- policies and automations;
- update jobs/history;
- Update Chains and Chain runs;
- verification profiles/history;
- restore-point metadata and integrity state;
- operation leases and update transaction stages.

Runtime secrets such as Docker TLS client material are stored separately under the persistent data directory and are not returned through ordinary host APIs.

## Docker connectivity

Supported connection modes are:

- local Unix socket;
- remote Docker TCP;
- Docker TLS/mTLS.

Vibewatch uses the Docker CLI/Engine interface for host inspection and lifecycle operations. Remote Docker TCP without TLS is inherently privileged and unencrypted; TLS/mTLS is the preferred remote transport.

## Watchtower worker model

Vibewatch uses the Nicholas Fedor Watchtower fork as a centrally managed, intentionally passive update worker. Vibewatch owns scheduling, Preflight, restore-point creation, verification, rollback and history. The worker is used for the actual image update operation and is not allowed to independently poll/update containers on its own schedule.

## Update transaction

A container update is represented as a persisted transaction with explicit stages. The important phases are:

```text
queued
 -> preflight
 -> snapshot
 -> restore_point
 -> prepared
 -> updating
 -> docker_health
 -> dependencies
 -> verifying
 -> refreshing
 -> success
```

Failure after mutation can enter rollback. v0.9.5 persists transaction state so a controller restart can reconcile the real Docker runtime instead of blindly declaring the update failed.

Before mutation, an interrupted transaction can be safely aborted. After mutation, Vibewatch first checks the current runtime and verification state. If the updated service is healthy, it can keep the successfully updated runtime. If it is not healthy and a valid restore point exists, Vibewatch can restore the pre-update state. An unresolved case is marked `recovery_required` and blocks another update of the same target until recovery is resolved.

## Preflight

Preflight is the safety gate before mutation. Checks include Docker/runtime conditions, registry/image information, restore-point readiness, Data Protection configuration and restore-storage capacity where applicable.

Checks use four presentation levels:

- Green: safe/pass.
- Info: informational, not a warning.
- Yellow: advisory warning.
- Red: blocker.

Automatic updates require a clean Preflight by default. Advisory warnings can be allowed explicitly; red blockers remain blocking.

## Restore points

A non-Swarm full restore point can contain:

- reconstructed runtime/config snapshot;
- the pre-update image/runtime reference;
- a paused `docker commit` snapshot of the container writable layer;
- selected persistent bind mounts / Docker volumes when Data Protection is enabled;
- dependency metadata for containers using another container's network namespace.

Restore points are retention-managed as one recovery unit. Config, writable-layer image and protected data are not treated as unrelated backup products.

### Data Protection helper

Persistent data is captured through short-lived helper containers created on the target Docker Engine. This allows remote-host files/volumes to remain on that host without SSH or a Vibewatch agent.

The helper is used for:

- host/restore filesystem probes;
- mount filesystem classification;
- local mount size measurement;
- archive creation/validation;
- data restore/cleanup.

Helper containers are labeled as Vibewatch system containers and are excluded from normal container management. v0.9.5 recovery maintenance removes orphaned helpers only after acquiring the same host-level operation lease used to protect destructive Docker operations.

### Local vs external storage

For bind mounts, Vibewatch inspects the real host source path, not only the container destination. Known local filesystems are classified Local; SMB/CIFS/NFS and other network/FUSE storage is External; ambiguous filesystems remain Unknown.

External storage can be explicitly protected, but Vibewatch cannot guarantee that writers outside the managed Docker Engine are stopped. External mount sizes are therefore not automatically traversed.

## Update Chains

Update Chains define an explicit order across services. Stack-scoped chains can own the update policy for a complete Compose stack; custom chains remain available for other relationships.

Vibewatch does not try to infer arbitrary application dependencies. The configured Chain order and per-step `skip`, `restart` or `recreate` behavior remain authoritative.

For automatic Chains, the complete update plan is Preflight-checked before the first mutation, and each actual update step runs its normal Preflight again immediately before execution.

### Chain crash recovery in v0.9.5

A Chain run stores its controller job ID and recovery context. After a controller restart:

1. child update transactions are reconciled first;
2. the Chain examines the recorded step/job outcomes;
3. a started restart/recreate step is verified or restored where a linked restore point exists;
4. shared protected-data baselines remain consistent with software rollback decisions;
5. remaining unstarted steps are marked interrupted and are not resumed automatically.

Chain history exposes `Recovered`, `Interrupted` and `Recovery required` states. A new Chain run is blocked while a previous run still requires recovery.

## Network namespace dependencies

Docker containers using another container's network namespace (for example Compose `network_mode: service:<parent>`) require special handling because the namespace is tied to the parent's container identity.

Vibewatch captures these relationships before replacing the parent, recreates dependents against the new parent container ID, and includes retained dependency snapshots in rollback protection.

## Verification

Custom Verification can run HTTP, HTTPS or TCP checks after an update or rollback. Profiles can be scoped to one container or an entire Compose stack.

Stack Verification has a shared stack state: a successful application-level check is displayed consistently across stack members unless a container has an explicit container-level override.

Verification is authoritative for application success where configured. A container can be Docker-running while the application endpoint still fails; that condition can trigger rollback.

## Operation leases

Destructive operations use persisted leases. Container-scoped update/rollback operations can run independently on unrelated containers, while host-scoped cleanup/recovery work conflicts with mutations on that host. Leases have heartbeats and expiry to avoid permanent locks after crashes.

## Recovery GC and cleanup

Recovery GC periodically:

- enforces restore/config retention;
- validates retained restore-point integrity;
- marks missing/incomplete recovery objects degraded or expired;
- removes Vibewatch-owned orphan restore images;
- removes orphaned short-lived helper containers when the host is idle.

`Cleanup unusable` is separate from normal retention. It may permanently remove expired/degraded recovery artifacts, but never Ready restore points and never a restore point referenced by an active/recovery-required transaction or Chain run.

## Automation

The scheduler supports:

- policy/update runs;
- cleanup runs using the same safe cleanup functions exposed on the Dashboard.

Cleanup never intentionally removes Vibewatch restore storage or restore images that are still protected by retained recovery state.

## Current boundaries

- Docker Swarm Data Protection remains config-only; full persistent-data restore is not enabled for Swarm services.
- Data Protection is update-recovery protection, not a substitute for independent backups.
- External/shared network storage cannot be made transactionally consistent with writers outside the Docker Engine controlled by Vibewatch.
- Vibewatch does not infer arbitrary application dependency graphs.
