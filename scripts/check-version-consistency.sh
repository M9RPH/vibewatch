#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"
version="$(tr -d '[:space:]' < VERSION)"
fail=0

check_fixed() {
  file="$1"
  needle="$2"
  if ! grep -Fq -- "$needle" "$file"; then
    printf 'version consistency: %s does not contain %s\n' "$file" "$needle" >&2
    fail=1
  fi
}

check_fixed web/package.json '"version": "'"$version"'"'
check_fixed Dockerfile "ARG VIBEWATCH_VERSION=$version"
check_fixed compose.yml "ghcr.io/m9rph/vibewatch:$version"
check_fixed docker-compose.yml "ghcr.io/m9rph/vibewatch:$version"
check_fixed docker-compose.build.yml "vibewatch:$version"
check_fixed .env.example "VIBEWATCH_APP_IMAGE=ghcr.io/m9rph/vibewatch:$version"
check_fixed scripts/generate-env.sh "printf '$version'"
check_fixed README.md "/v$version/compose.yml"
check_fixed README.md "/v$version/.env.example"
check_fixed docs/INSTALLATION.md "/v$version/compose.yml"
check_fixed docs/INSTALLATION.md "ghcr.io/m9rph/vibewatch:$version"
check_fixed web/src/App.tsx "v$version"
check_fixed web/src/App.tsx "'$version'"
check_fixed RELEASE_NOTES.md "# Vibewatch v$version"
check_fixed CHANGELOG.md "## $version"
check_fixed .vibewatch-internal/VERSION_CONTEXT "$version"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
printf 'version consistency: %s OK\n' "$version"
