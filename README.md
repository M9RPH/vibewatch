# Vibewatch

Vibewatch is a multi-host Docker update controller focused on **safe, observable and recoverable** container updates.

The name combines **vibe coding** and **Watchtower**: Vibewatch grew out of AI-assisted development and its roots in the Watchtower ecosystem.

## Install

Vibewatch publishes release images to **GHCR** for `linux/amd64` and `linux/arm64`.

```bash
mkdir -p vibewatch && cd vibewatch
curl -fsSLo compose.yml https://raw.githubusercontent.com/M9RPH/vibewatch/v1.0.17/compose.yml
curl -fsSLo .env https://raw.githubusercontent.com/M9RPH/vibewatch/v1.0.17/.env.example
```

Set at least these values in `.env`:

```dotenv
VIBEWATCH_ADMIN_PASSWORD=replace-with-a-strong-password
VIBEWATCH_SESSION_SECRET=replace-with-a-long-random-secret
```

Start Vibewatch:

```bash
docker compose pull
docker compose up -d
```

Open `http://<docker-host>:8085`.
Initial Owner login:

- **User:** `admin`
- **Password:** value of `VIBEWATCH_ADMIN_PASSWORD`

## Documentation

- [Installation](docs/INSTALLATION.md)
- [Update pipeline and Preflight](docs/UPDATE_PIPELINE.md)
- [Verification](docs/VERIFICATION.md)
- [Data Protection and rollback](docs/DATA_PROTECTION_AND_ROLLBACK.md)
- [Recovery and crash safety](docs/RECOVERY_AND_CRASH_SAFETY.md)
- [Update Chains](docs/UPDATE_CHAINS.md)
- [Automation](docs/AUTOMATION.md)
- [Security](docs/SECURITY.md)
- [UI screenshots](docs/SCREENSHOTS.md)
- [Architecture](ARCHITECTURE.md)
- [Changelog](CHANGELOG.md)
- [Release notes](RELEASE_NOTES.md)


## Screenshots

A few current **Web UI v2** views:

| Dashboard | Containers |
| --- | --- |
| ![Dashboard](docs/screenshots/dashboard.png) | ![Containers](docs/screenshots/containers.png) |

| Hosts | Update Chains |
| --- | --- |
| ![Hosts](docs/screenshots/hosts.png) | ![Update Chains](docs/screenshots/update-chains.png) |

More screenshots: [docs/SCREENSHOTS.md](docs/SCREENSHOTS.md)

## License

[MIT](LICENSE)
