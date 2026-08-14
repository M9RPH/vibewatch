# Installation and upgrades

## Supported installation

The recommended installation is Docker Compose using the published GHCR image.

### Requirements

- Docker Engine
- Docker Compose v2
- a persistent directory for Vibewatch `/data`
- local Docker socket access for the controller host

### Install

```bash
cp .env.example .env
```

Set at least:

```dotenv
VIBEWATCH_ADMIN_PASSWORD=<strong-password>
VIBEWATCH_SESSION_SECRET=<long-random-secret>
```

Generate a session secret if needed:

```bash
openssl rand -hex 32
```

Start:

```bash
docker compose pull
docker compose up -d
```

Default web port: `8085`.

Initial Owner login:

```text
username: admin
password: VIBEWATCH_ADMIN_PASSWORD from .env
```

## Persistence

The default Compose file maps:

```text
./data -> /data
```

Do not delete or replace this directory during an upgrade. It contains the SQLite database, logs, controller backup bundles and Docker TLS/mTLS client material.

## Adding hosts

Use **Hosts** in the web UI.

- Local socket: `unix:///var/run/docker.sock`
- Remote legacy TCP: `tcp://host:2375`
- Remote TLS/mTLS: `tls://host:2376`

TLS/mTLS is recommended for remote Docker Engines. Port 2375 is unencrypted and grants highly privileged Docker access.

## Upgrade

The release Compose file is version-pinned. To upgrade after replacing the repository/Compose files with the new release:

```bash
docker compose pull
docker compose up -d
```

Vibewatch applies additive SQLite migrations on startup.

Before a major upgrade, keep an independent copy of your Vibewatch data directory. Owner Settings can also create a portable Vibewatch backup bundle.

## Local source build

Development/source builds can use the build overlay:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

The public release image remains the recommended deployment path.
## Upgrading from legacy `WTUI_*` variables

Vibewatch v0.9.5 uses `VIBEWATCH_*` as the canonical environment-variable prefix. Existing installations do **not** need to rewrite their live `.env` immediately: the pre-rebrand `WTUI_*` names remain accepted as compatibility fallbacks.

For a planned cleanup, rename variables one-for-one:

```text
WTUI_PORT             -> VIBEWATCH_PORT
WTUI_DATA_PATH        -> VIBEWATCH_DATA_PATH
WTUI_ADMIN_PASSWORD   -> VIBEWATCH_ADMIN_PASSWORD
WTUI_SESSION_SECRET   -> VIBEWATCH_SESSION_SECRET
WTUI_LOG_LEVEL        -> VIBEWATCH_LOG_LEVEL
WTUI_WATCHTOWER_IMAGE -> VIBEWATCH_WATCHTOWER_IMAGE
WTUI_APP_IMAGE        -> VIBEWATCH_APP_IMAGE
```

If both names are present, the `VIBEWATCH_*` value wins.

