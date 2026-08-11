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
WTUI_PORT=8085
TZ=Europe/Berlin
WTUI_DATA_PATH=./data
WTUI_ADMIN_PASSWORD=$password
WTUI_SESSION_SECRET=$secret
WTUI_LOG_LEVEL=INFO
WTUI_WATCHTOWER_IMAGE=nickfedor/watchtower:latest
GITHUB_TOKEN=
WTUI_APP_IMAGE=
EOT
chmod 600 .env
echo ".env created"
