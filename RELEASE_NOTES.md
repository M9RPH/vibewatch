# Vibewatch v1.0.0

Vibewatch v1.0.0 is the first stable public release of the project.

## Highlights

- New **Web UI v2** with the final Vibewatch design language.
- Live staged inspectors for **Preflight**, **SSH Quick Setup**, **container updates** and **rollbacks**.
- Hardened **SSH Quick Setup** for legacy TCP and managed mTLS, including transactional rollback and richer diagnostics.
- Improved **Container Inspector** with service icons, update classification and direct safety actions.
- Owner-only **Developer Updater** for applying development ZIP packages from the UI.
- Persistent notification read-state and navigation from notifications to the relevant object.
- Public GitHub release layout with updated docs, screenshots, CI and container publishing.

## Upgrade

The database migration path remains additive. Existing hosts, policies, Update Chains, verification profiles, restore points and history remain compatible.

```bash
docker compose pull
docker compose up -d
```

The release Compose file is pinned to:

```text
ghcr.io/m9rph/vibewatch:1.0.0
```

## Distribution

Vibewatch can be installed without cloning the source tree.

```bash
mkdir -p vibewatch && cd vibewatch
curl -fsSLo compose.yml https://raw.githubusercontent.com/M9RPH/vibewatch/v1.0.0/compose.yml
curl -fsSLo .env https://raw.githubusercontent.com/M9RPH/vibewatch/v1.0.0/.env.example
nano .env
docker compose pull
docker compose up -d
```
