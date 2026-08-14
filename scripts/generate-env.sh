#!/bin/sh
set -eu
password="${1:-}"
if [ -z "$password" ]; then
  printf "Admin password: " >&2
  stty -echo
  read password
  stty echo
  printf "\n" >&2
fi
secret=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
cat > .env <<EOT
VIBEWATCH_PORT=8085
TZ=Europe/Berlin
VIBEWATCH_DATA_PATH=./data
VIBEWATCH_ADMIN_PASSWORD=$password
VIBEWATCH_SESSION_SECRET=$secret
VIBEWATCH_LOG_LEVEL=INFO
VIBEWATCH_WATCHTOWER_IMAGE=nickfedor/watchtower:latest
GITHUB_TOKEN=
VIBEWATCH_APP_IMAGE=ghcr.io/m9rph/vibewatch:0.9.5
EOT
chmod 600 .env
echo ".env created"
