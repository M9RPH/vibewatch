# Vibewatch v0.9.5

v0.9.5 is a reliability release focused on **crash-safe update recovery**.

## Highlights

- Update Chains now survive controller restarts with persisted recovery context instead of being blindly marked failed.
- Child update transactions reconcile before the Chain is evaluated.
- Started Chain work is verified/restored where possible; remaining unstarted steps are deliberately not resumed automatically.
- Chain History can show **Recovered**, **Interrupted** and **Recovery required**.
- Unresolved recovery state blocks new updates/runs for the affected target.
- Successful explicit rollback resolves a recovery-required transaction linked to that restore point.
- Recovery GC removes orphaned Vibewatch helper containers on idle hosts.
- Container Rollback adds **Cleanup unusable** for expired/degraded recovery artifacts while protecting Ready and transaction-referenced restore points.
- Public README/architecture/docs have been rewritten for the GitHub release.
- The canonical environment prefix is now `VIBEWATCH_*`; existing `WTUI_*` values continue to work as upgrade fallbacks.
- The Go module path now matches the public repository: `github.com/m9rph/vibewatch`.
- Fixed persisted Restore Point JSON hydration for dependency metadata and Data Protection manifests, including the GitHub Actions `TestDependencyMetadataRoundTrip` failure.

## Upgrade

The database migration is additive and automatic. Existing hosts, policies, Update Chains, Data Protection profiles and restore points remain compatible.

Source builds remain compatible with the project's ES2020 frontend target; the release also fixes a Compose interpolation warning in the legacy runtime-migration helper.

```bash
docker compose pull
docker compose up -d
```

The release Compose file is pinned to:

```text
ghcr.io/m9rph/vibewatch:0.9.5
```

## Notes

Vibewatch remains pre-1.0 software. Review unattended update policies and keep independent backups of critical application data.

## Docker Compose / GHCR installation

Vibewatch v0.9.5 can be installed without cloning or building the source tree. The release workflow publishes multi-architecture images for `linux/amd64` and `linux/arm64` to `ghcr.io/m9rph/vibewatch`. The repository includes an official `compose.yml` pinned to this release.

```bash
mkdir -p vibewatch && cd vibewatch
curl -fsSLo compose.yml https://raw.githubusercontent.com/M9RPH/vibewatch/v0.9.5/compose.yml
curl -fsSLo .env https://raw.githubusercontent.com/M9RPH/vibewatch/v0.9.5/.env.example
nano .env
docker compose pull
docker compose up -d
```
