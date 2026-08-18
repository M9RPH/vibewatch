# Security model

## Docker access is privileged

Anyone who can control a Docker Engine can generally obtain root-equivalent control of that host. Vibewatch therefore must be treated as privileged infrastructure software.

Do not expose an unauthenticated Docker API to untrusted networks.

## Remote Docker

Preferred remote mode is **Secure connection**. Owner-only Secure Quick Setup uses SSH once to configure Docker on `2376` with mutual TLS, generates a per-host CA plus server/client identities and stores only the controller credentials in Vibewatch persistent storage.

If a host already has TLS/mTLS configuration owned by another application, Vibewatch refuses to overwrite it. Use the Advanced connection path with an existing CA/client certificate/key instead. Secure and Legacy TCP Quick Setup also preflight Docker's existing systemd/daemon configuration before changing it. Remote changes are staged transactionally: the previous Docker configuration is retained until the controller has successfully reached the new endpoint, and failures restore the previous listener automatically when possible.

Legacy unencrypted `tcp://host:2375` remains supported for compatibility but should only exist on a trusted isolated network.

## Controller secrets

- Keep `.env` private.
- Use a strong Owner password.
- Use a long random session secret.
- Docker TLS/mTLS private keys are stored in the persistent data directory with restrictive permissions. Vibewatch-managed Secure Quick Setup also retains the per-host CA key so future credential rotation can preserve host identity.
- Registry credentials are stored encrypted by Vibewatch.
- Review diagnostic/support archives before publishing them.

## Data Protection helper

The short-lived helper can mount selected host paths/volumes because Vibewatch already holds privileged Docker access. The helper runs without a network, uses a read-only helper filesystem where possible and is removed after the operation. Orphaned helpers are reconciled by Recovery GC/startup recovery.

## Permissions

Vibewatch provides Owner, Admin and User roles plus host/group visibility. Destructive configuration and recovery operations require elevated roles.

## Backups

Vibewatch restore points protect updates; they are not an independent disaster-recovery backup. Keep separate backups of critical application data and the Vibewatch persistent data directory.
