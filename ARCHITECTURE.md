# Architecture

```
 fleet-sim (4 nodes × 8 GPUs)          Prometheus                     Alertmanager              alert-sink
 :9401 gpu-node-1 /metrics  ──┐   scrape every 5s (lab)      firing    route by severity ──▶ /pager   (critical)
 :9402 gpu-node-2 /metrics  ──┼──▶ recording rules ────────▶ alerts ──▶ group, inhibit ──────▶ /ticket  (warning)
 :9403 gpu-node-3 /metrics  ──┤   alerting rules                                         └──▶ /owners  (info, per namespace)
 :9404 gpu-node-4 /metrics  ──┘        │
 :9400 control API (inject faults)      ├──▶ Grafana "GPU fleet" dashboard
                                        └──▶ fleetreport (Markdown / JSON)
```

| Component | In the lab | In a real cluster |
|---|---|---|
| Telemetry | `fleet-sim`: one port per node, dcgm-exporter metric names and labels | dcgm-exporter DaemonSet (GPU Operator), pod labels from the kubelet pod-resources API |
| Scraping | Static targets with a `Hostname` label | PodMonitor/ServiceMonitor, relabelling the node name |
| Rules | `prometheus/rules/*.yml`, tested with `promtool test rules` | Same files, as PrometheusRule objects |
| Routing | Webhooks to `alert-sink` | Paging tool, ticket queue, team chat channels |
| Dashboard | Provisioned from `grafana/dashboards/gpu-fleet.json` | Same JSON, via the Grafana sidecar or provisioning |
| Report | `fleetreport` | A scheduled job posting to the team channel or a weekly review |

## Simulated fleet

| Node | GPUs 0–3 | GPUs 4–7 |
|---|---|---|
| gpu-node-1 | `ml-training/llm-pretrain-worker-0`, ~92% | same pod |
| gpu-node-2 | `ml-training/llm-pretrain-worker-1`, ~92% | same pod |
| gpu-node-3 | `serving/chat-api-0..3`, 25–65% following a daily wave | free |
| gpu-node-4 | 0–1: `serving/embeddings-*`; 2–3: `research/notebook-a-7f9c`, 0% while holding 42 GB | free |

Power follows utilisation (about 70 W idle to 690 W at 100%), temperature follows power, and the energy counter integrates power over time, so `rate()` of the counter matches the power gauge.

## Fault injection

`POST :9400/fault?node=…&gpu=…` with any of `xid=`, `dbe=`, `remap=1`, `temp=+N`, `nvlink=N/s`; `POST /exporter?node=…&down=1` makes a node's endpoint answer 503; `POST /workload` reassigns a GPU; `POST /reset` restores the defaults. `scripts/inject.sh` wraps the common cases.

## Routing

```
severity=critical ──▶ pager      group by alertname + node
severity=warning  ──▶ ticket     (default route)
severity=info     ──▶ owners     group by namespace: one message per team

inhibit: critical with action=drain|quarantine  ⊣ GPUAllocatedButIdle   (same node + GPU)
         GPUTemperatureCritical                 ⊣ GPUTemperatureHigh    (same node + GPU)
```
