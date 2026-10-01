#!/usr/bin/env bash
# "cond && pass || fail" is safe here (pass always returns 0); the $ in single quotes is Python code.
# shellcheck disable=SC2015,SC2016
# End-to-end checks against a running lab (docker compose up, or the binaries started by hand).
# Each step injects something through the simulator or the Alertmanager API and checks the result
# in Prometheus, Alertmanager and the alert sink.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PROM=${PROM_URL:-http://localhost:9090}
AM=${AM_URL:-http://localhost:9093}
SIM=${SIM_URL:-http://localhost:9400}
SINK=${SINK_URL:-http://localhost:9099}
GRAFANA=${GRAFANA_URL-http://localhost:3000}   # set to "" to skip the Grafana checks
REPORT=${FLEETREPORT:-docker compose run --rm --no-deps fleet-sim /usr/local/bin/fleetreport -prometheus http://prometheus:9090}

PASSED=0
FAILED=0
step() { printf '\n==> %s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$*"; PASSED=$((PASSED + 1)); }
fail() { printf '  FAIL  %s\n' "$*"; FAILED=$((FAILED + 1)); }

# q EXPR -> first value of an instant query, or "none"
q() {
  curl -fsS -G "$PROM/api/v1/query" --data-urlencode "query=$1" | python3 -c '
import json, sys
r = json.load(sys.stdin)["data"]["result"]
print(r[0]["value"][1] if r else "none")'
}
# approx A B TOLERANCE -> true when |A-B| <= TOLERANCE
approx() { python3 -c "import sys; a,b,t=map(float,sys.argv[1:]); sys.exit(0 if abs(a-b)<=t else 1)" "$1" "$2" "$3" 2>/dev/null; }
sim() { curl -fsS -X POST "$SIM$1" >/dev/null; }
# firing NAME [matchers] -> number of firing alerts
firing() { q "count(ALERTS{alertname=\"$1\",alertstate=\"firing\"$2}) or vector(0)"; }
# received RECEIVER ALERTNAME [STATUS] -> how many times the sink got it
received() {
  curl -fsS "$SINK/received" | python3 -c '
import json, sys
recv, name, status = sys.argv[1], sys.argv[2], sys.argv[3]
got = json.load(sys.stdin) or []
print(sum(1 for a in got if a["receiver"] == recv and a["labels"].get("alertname") == name and (status == "" or a["status"] == status)))' "$1" "$2" "${3:-}"
}
# wait_for SECONDS COMMAND... -> retries until the command succeeds
wait_for() {
  local t=$1 i
  shift
  for ((i = 0; i < t; i += 3)); do "$@" && return 0; sleep 3; done
  return 1
}
is() { [ "$(eval "$1")" = "$2" ]; }
ge() { python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) >= float(sys.argv[2]) else 1)" "$(eval "$1")" "$2" 2>/dev/null; }

# Post a synthetic alert straight to Alertmanager (for alerts whose real 'for:' is 30+ minutes).
am_alert() {
  local labels=$1
  curl -fsS -X POST "$AM/api/v2/alerts" -H 'Content-Type: application/json' -d "[{
    \"labels\": $labels,
    \"annotations\": {\"summary\": \"synthetic alert from scripts/test.sh\"},
    \"endsAt\": \"$(date -u -d '+10 minutes' +%Y-%m-%dT%H:%M:%SZ)\"}]" >/dev/null
}

# ---------------------------------------------------------------------------------------------
step "0. Lab is up"
sim /reset
curl -fsS -X DELETE "$SINK/received" >/dev/null
if wait_for 120 is 'q "count(up{job=\"dcgm-exporter\"} == 1)"' 4 && wait_for 60 is 'q "fleet:gpus:count"' 32; then
  pass "4 exporter targets up, 32 GPUs reporting"
else
  fail "lab not ready: targets up $(q 'count(up{job="dcgm-exporter"} == 1)'), GPUs $(q 'fleet:gpus:count')"
fi

# ---------------------------------------------------------------------------------------------
step "1. Recording rules match the simulated fleet"
is 'q fleet:gpus_allocated:count' 24 && pass "24 GPUs allocated to pods" || fail "allocated: $(q fleet:gpus_allocated:count)"
is 'q fleet:gpus_unhealthy:count' 0 && pass "no unhealthy GPUs" || fail "unhealthy: $(q fleet:gpus_unhealthy:count)"
is 'q fleet:gpus_healthy:ratio' 1 && pass "healthy capacity 100%" || fail "healthy ratio: $(q fleet:gpus_healthy:ratio)"
wait_for 30 is 'q fleet:gpus_allocated_idle:count' 2 && pass "2 GPUs held by an idle notebook (research/notebook-a-7f9c)" ||
  fail "allocated idle: $(q fleet:gpus_allocated_idle:count)"
ml=$(q 'namespace:gpu_utilization:avg{namespace="ml-training"}')
ge "echo $ml" 80 && pass "ml-training utilisation ${ml%.*}%" || fail "ml-training utilisation $ml"
# The energy counter (mJ) and the power gauge (W) must tell the same story. irate uses the last two
# scrapes, so this works seconds after start; the 5m recording rule needs 5 minutes of data.
pw=$(q fleet:gpu_power_watts:sum)
en=$(q 'sum(irate(DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION[1m])) / 1000')
approx "$pw" "$en" "$(python3 -c "print(float('$pw')*0.1)")" && pass "energy counter agrees with power gauge (${pw%.*} W vs ${en%.*} W)" ||
  fail "power ${pw} W vs energy rate ${en} W"

# ---------------------------------------------------------------------------------------------
step "2. Application XID 13 on gpu-node-1: no alert, not unhealthy"
sim "/fault?node=gpu-node-1&gpu=0&xid=13"
sleep 15
[ "$(q 'count(ALERTS{alertstate="firing",Hostname="gpu-node-1"}) or vector(0)')" = 0 ] && [ "$(q fleet:gpus_unhealthy:count)" = 0 ] &&
  pass "nothing fires for an application error" || fail "something fired: $(q 'ALERTS{Hostname="gpu-node-1"}')"
sim "/fault?node=gpu-node-1&gpu=0&xid=0"

# ---------------------------------------------------------------------------------------------
step "3. XID 79 on gpu-node-2 GPU 3: critical alert, routed to the pager"
sim "/fault?node=gpu-node-2&gpu=3&xid=79"
wait_for 45 is 'firing GPUFellOffBus ",Hostname=\"gpu-node-2\",gpu=\"3\""' 1 && pass "GPUFellOffBus firing" || fail "GPUFellOffBus not firing"
wait_for 30 ge 'received pager GPUFellOffBus firing' 1 && pass "delivered to the pager receiver" || fail "not delivered to pager"
[ "$(received ticket GPUFellOffBus)" = 0 ] && pass "not sent to the ticket queue" || fail "also sent to ticket"
is 'q fleet:gpus_unhealthy:count' 1 && pass "counted as unhealthy" || fail "unhealthy: $(q fleet:gpus_unhealthy:count)"

# ---------------------------------------------------------------------------------------------
step "4. Row remapping failure on gpu-node-4 GPU 0: quarantine"
sim "/fault?node=gpu-node-4&gpu=0&remap=1"
wait_for 45 is 'firing GPUMemoryUnrepairable ",action=\"quarantine\",Hostname=\"gpu-node-4\""' 1 && pass "GPUMemoryUnrepairable firing with action=quarantine" ||
  fail "GPUMemoryUnrepairable not firing"
wait_for 30 ge 'received pager GPUMemoryUnrepairable firing' 1 && pass "delivered to the pager receiver" || fail "not delivered"

# ---------------------------------------------------------------------------------------------
step "5. Inhibition and team routing (synthetic alerts posted to Alertmanager)"
# Owners hear about their idle GPU, unless the GPU is broken (gpu-node-2 GPU 3 fell off the bus).
am_alert '{"alertname":"GPUAllocatedButIdle","severity":"info","Hostname":"gpu-node-2","gpu":"3","namespace":"ml-training","pod":"llm-pretrain-worker-1"}'
am_alert '{"alertname":"GPUAllocatedButIdle","severity":"info","Hostname":"gpu-node-4","gpu":"2","namespace":"research","pod":"notebook-a-7f9c"}'
# The critical temperature alert hides the warning for the same GPU; another GPU's warning gets through.
am_alert '{"alertname":"GPUTemperatureCritical","severity":"critical","action":"cordon","Hostname":"gpu-node-1","gpu":"5"}'
am_alert '{"alertname":"GPUTemperatureHigh","severity":"warning","Hostname":"gpu-node-1","gpu":"5"}'
am_alert '{"alertname":"GPUTemperatureHigh","severity":"warning","Hostname":"gpu-node-1","gpu":"6"}'
owners() {
  curl -fsS "$SINK/received" | python3 -c '
import json, sys
got = [a for a in (json.load(sys.stdin) or []) if a["receiver"] == sys.argv[1] and a["labels"].get("alertname") == sys.argv[2]]
print(",".join(sorted(a["labels"]["Hostname"] + "/" + a["labels"]["gpu"] for a in got)))' "$@"
}
wait_for 40 is 'owners owners GPUAllocatedButIdle' "gpu-node-4/2" && pass "owners told about research's idle GPU only" ||
  fail "owners got: $(owners owners GPUAllocatedButIdle)"
inhibited=$(curl -fsS -G "$AM/api/v2/alerts" --data-urlencode 'filter=alertname="GPUAllocatedButIdle"' --data-urlencode 'filter=Hostname="gpu-node-2"' |
  python3 -c 'import json,sys; a=json.load(sys.stdin); print(len(a[0]["status"]["inhibitedBy"]) if a else -1)')
[ "$inhibited" -ge 1 ] && pass "idle alert on the broken GPU is inhibited by GPUFellOffBus" || fail "inhibitedBy: $inhibited"
wait_for 40 is 'owners ticket GPUTemperatureHigh' "gpu-node-1/6" && pass "warning for GPU 6 went to the ticket queue; GPU 5's was inhibited" ||
  fail "ticket got: $(owners ticket GPUTemperatureHigh)"
wait_for 20 ge 'received pager GPUTemperatureCritical firing' 1 && pass "critical temperature paged" || fail "critical temperature not paged"

# ---------------------------------------------------------------------------------------------
step "6. Exporter on gpu-node-3 stops answering"
sim "/exporter?node=gpu-node-3&down=1"
wait_for 120 is 'firing GPUExporterDown ",Hostname=\"gpu-node-3\""' 1 && pass "GPUExporterDown firing after 1 minute" || fail "GPUExporterDown not firing"
is 'q fleet:gpus:count' 24 && pass "24 GPUs reporting" || fail "GPUs reporting: $(q fleet:gpus:count)"
r=$(q fleet:gpus_healthy:ratio)
approx "$r" 0.6875 0.001 && pass "healthy capacity 68.75% (22 of 32 expected)" || fail "healthy ratio $r"
[ "$(q 'count(ALERTS{alertname="GPUFleetHealthyCapacityLow"}) or vector(0)')" = 1 ] && pass "GPUFleetHealthyCapacityLow pending (fires after 5 minutes)" ||
  fail "capacity alert missing"

# ---------------------------------------------------------------------------------------------
step "7. Fleet report"
report=$($REPORT -window 1h 2>&1)
echo "$report" | sed 's/^/    /' | head -40
for want in "research/notebook-a-7f9c" "| gpu-node-2 | 3 | XID 79 |" "| gpu-node-4 | 0 | row remapping failure |" "GPUExporterDown (critical)"; do
  case "$report" in *"$want"*) pass "report lists: $want" ;; *) fail "report misses: $want" ;; esac
done

# ---------------------------------------------------------------------------------------------
if [ -n "$GRAFANA" ]; then
  step "8. Grafana"
  wait_for 60 curl -fsS "$GRAFANA/api/health" >/dev/null && pass "Grafana healthy" || fail "Grafana not healthy"
  found=$(curl -fsS "$GRAFANA/api/search?query=GPU%20fleet" | python3 -c 'import json,sys; print(sum(1 for d in json.load(sys.stdin) if d.get("uid")=="gpu-fleet"))')
  [ "$found" = 1 ] && pass "dashboard provisioned" || fail "dashboard not found"
  ds=$(curl -fsS -u "admin:${GRAFANA_ADMIN_PASSWORD:-change-me}" "$GRAFANA/api/datasources/uid/prometheus/health" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status"))')
  [ "$ds" = OK ] && pass "Prometheus data source reachable from Grafana" || fail "data source health: $ds"
fi

step "9. Every dashboard query is valid and returns data"
bad=0
total=0
while IFS= read -r expr; do
  total=$((total + 1))
  res=$(curl -sS -G "$PROM/api/v1/query" --data-urlencode "query=$expr" | python3 -c '
import json, sys
r = json.load(sys.stdin)
print("error: " + r.get("error", "") if r["status"] != "success" else ("empty" if not r["data"]["result"] else "ok"))')
  if [ "$res" != ok ]; then bad=$((bad + 1)); echo "    $res: $expr"; fi
done < <(python3 -c '
import json
d = json.load(open("grafana/dashboards/gpu-fleet.json"))
for p in d["panels"]:
    for t in p.get("targets", []):
        print(t["expr"].replace("$node", ".*"))')
[ "$bad" = 0 ] && pass "$total panel queries ok" || fail "$bad of $total panel queries failed or returned nothing"

# ---------------------------------------------------------------------------------------------
step "10. Recovery"
sim /reset
wait_for 90 ge 'received pager GPUFellOffBus resolved' 1 && pass "pager got the resolve for GPUFellOffBus" || fail "no resolve sent"
wait_for 60 is 'q fleet:gpus_healthy:ratio' 1 && pass "healthy capacity back to 100%" || fail "healthy ratio $(q fleet:gpus_healthy:ratio)"

printf '\n%d passed, %d failed\n' "$PASSED" "$FAILED"
[ "$FAILED" -eq 0 ]
