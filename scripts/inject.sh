#!/usr/bin/env bash
# Shortcuts for breaking the simulated fleet by hand.
#   scripts/inject.sh xid79        GPU 3 of gpu-node-2 falls off the bus
#   scripts/inject.sh ecc          2 uncorrectable ECC errors on gpu-node-3 GPU 7
#   scripts/inject.sh remap        row remapping failure on gpu-node-4 GPU 0
#   scripts/inject.sh hot          gpu-node-1 GPU 5 runs 40°C hotter (alerts after 2-5 minutes)
#   scripts/inject.sh nvlink       NVLink CRC errors on gpu-node-1 GPU 6 (alerts after 10 minutes)
#   scripts/inject.sh exporter     gpu-node-3 stops reporting
#   scripts/inject.sh idle         a forgotten pod grabs gpu-node-3 GPU 6
#   scripts/inject.sh reset        everything back to normal
set -euo pipefail
SIM=${SIM_URL:-http://localhost:9400}
post() { curl -fsS -X POST "$SIM$1"; }
case "${1:-}" in
  xid79) post "/fault?node=gpu-node-2&gpu=3&xid=79" ;;
  ecc) post "/fault?node=gpu-node-3&gpu=7&dbe=2" ;;
  remap) post "/fault?node=gpu-node-4&gpu=0&remap=1" ;;
  hot) post "/fault?node=gpu-node-1&gpu=5&temp=40" ;;
  nvlink) post "/fault?node=gpu-node-1&gpu=6&nvlink=5" ;;
  exporter) post "/exporter?node=gpu-node-3&down=1" ;;
  idle) post "/workload?node=gpu-node-3&gpu=6&profile=stuck&namespace=research&pod=forgotten-job" ;;
  reset) post "/reset" ;;
  *) sed -n '2,11p' "$0"; exit 2 ;;
esac
