#!/usr/bin/env bash
# Starts the lab: simulated fleet, Prometheus, Alertmanager, alert sink and Grafana.
set -euo pipefail
cd "$(dirname "$0")/.." || exit 1
docker compose up -d --build
for i in $(seq 1 60); do
  if curl -fsS http://localhost:9090/-/ready >/dev/null 2>&1 && curl -fsS http://localhost:9400/healthz >/dev/null 2>&1; then
    echo "Lab ready:"
    echo "  Grafana       http://localhost:3000   (dashboard 'GPU fleet', read-only without login)"
    echo "  Prometheus    http://localhost:9090/alerts"
    echo "  Alertmanager  http://localhost:9093"
    echo "  Simulator     http://localhost:9400/state"
    exit 0
  fi
  sleep 2
  [ "$i" = 60 ] && { docker compose ps; exit 1; }
done
