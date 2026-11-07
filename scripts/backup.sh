#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
target="${1:-.local/backups/$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$target"
docker compose stop api
trap 'docker compose start api >/dev/null' EXIT
docker compose exec -T db pg_dump -U ledger -d ledger -Fc > "$target/database.dump"
docker compose run --rm --no-deps -T --user 0 --entrypoint tar api -C /data/attachments -czf - . > "$target/attachments.tar.gz"
echo "Backup written to $target"
