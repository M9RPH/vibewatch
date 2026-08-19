# Installation and upgrades

## Recommended installation: Docker Compose + GHCR

Vibewatch release images are published to GitHub Container Registry for `linux/amd64` and `linux/arm64`. A Git clone and local Go/Node build are not required for a normal installation.

### Requirements

- Docker Engine
- Docker Compose v2
- a persistent directory for Vibewatch `/data`
- local Docker socket access for the controller host

### Quick install without cloning the repository

```bash
mkdir -p vibewatch && cd vibewatch
curl -fsSLo compose.yml https://raw.githubusercontent.com/M9RPH/vibewatch/v1.0.18/compose.yml
curl -fsSLo .env https://raw.githubusercontent.com/M9RPH/vibewatch/v1.0.18/.env.example
```

Edit `.env` and set at least:

```dotenv
VIBEWATCH_ADMIN_PASSWORD=<strong-password>
VIBEWATCH_SESSION_SECRET=<long-random-secret>
```

Generate a session secret if needed:

```bash
openssl rand -hex 32
```

Start Vibewatch:

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

The v1.0.18 Compose file is pinned to:

```text
ghcr.io/m9rph/vibewatch:1.0.18
```

A public GHCR image can be pulled anonymously; no GitHub login is required on the Docker host.

## Portainer Stack

The same `compose.yml` can be pasted into **Stacks → Add stack** in Portainer. Set the required `VIBEWATCH_*` values in Portainer's stack environment before deploying.

At minimum provide:

```dotenv
VIBEWATCH_ADMIN_PASSWORD=<strong-password>
VIBEWATCH_SESSION_SECRET=<long-random-secret>
```

The local Docker socket bind in the Compose file gives Vibewatch access to the Docker Engine hosting the controller.

## Persistence

The default Compose file maps:

```text
./data -> /data
```

Do not delete or replace this directory during an upgrade. It contains the SQLite database, logs, controller backup bundles and Docker TLS/mTLS client material.

## Adding hosts

Use **Hosts** in the web UI.

- Local socket: `unix:///var/run/docker.sock`
- Secure remote connection: use **Secure Quick Setup** (recommended; Docker `2376` with mTLS)
- Existing secure endpoint: `tls://host:2376` through Advanced connection options
- Remote legacy TCP: `tcp://host:2375`

Secure Quick Setup requires SSH/sudo access once during bootstrap. Vibewatch creates and manages the certificates automatically. Existing TLS/mTLS daemon configuration is detected and is not overwritten. Port 2375 is unencrypted and grants highly privileged Docker access.

## Upgrade

Each release Compose file is version-pinned. For a normal upgrade, update `VIBEWATCH_APP_IMAGE` in `.env` to the new release tag and run:

```bash
docker compose pull
docker compose up -d
```

Alternatively replace `compose.yml` with the file from the new release before running the same commands.

Vibewatch applies additive SQLite migrations on startup.

Before a major upgrade, keep an independent copy of your Vibewatch data directory. Owner Settings can also create a portable Vibewatch backup bundle.

## Build from source

Development/source builds use the repository build overlay:

```bash
git clone https://github.com/M9RPH/vibewatch.git
cd vibewatch
cp .env.example scripts/.env
cd scripts
docker compose --env-file .env -f ../docker-compose.yml -f ../docker-compose.build.yml up -d --build
```

The published GHCR image remains the recommended deployment path.

## Upgrading from legacy `WTUI_*` variables

Vibewatch v1.0.0 uses `VIBEWATCH_*` as the canonical environment-variable prefix. Existing installations do **not** need to rewrite their live `.env` immediately: the pre-rebrand `WTUI_*` names remain accepted as compatibility fallbacks.

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


## Owner developer updater

Development builds can also be applied from **Settings → Developer**. This owner-only flow uploads a prepared Vibewatch ZIP package, validates the package and performs the controller self-update with automatic source rollback if the replacement build fails. It is intended for development workflows and is not required for normal release upgrades.
