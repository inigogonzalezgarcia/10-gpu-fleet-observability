#!/usr/bin/env bash
# Stops the lab and deletes its data (the next start is clean).
set -euo pipefail
cd "$(dirname "$0")/.." || exit 1
docker compose down -v
