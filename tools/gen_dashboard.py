"""Generates grafana/dashboards/gpu-fleet.json. Edit here, not in the JSON.

    python3 tools/gen_dashboard.py
"""
import json
import pathlib

DS = {"type": "prometheus", "uid": "prometheus"}
panels, y = [], 0
pid = 0


def nid():
    global pid
    pid += 1
    return pid


def row(title):
    global y
    panels.append({"type": "row", "title": title, "id": nid(), "collapsed": False,
                   "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}, "panels": []})
    y += 1


def stat(title, expr, x, w=4, unit="none", decimals=None, thresholds=None, desc=""):
    p = {"type": "stat", "title": title, "id": nid(), "datasource": DS, "description": desc,
         "gridPos": {"h": 4, "w": w, "x": x, "y": y},
         "targets": [{"refId": "A", "datasource": DS, "expr": expr, "instant": True}],
         "options": {"reduceOptions": {"calcs": ["lastNotNull"]}, "colorMode": "background", "graphMode": "none"},
         "fieldConfig": {"defaults": {"unit": unit, "thresholds": {"mode": "absolute", "steps":
                         thresholds or [{"color": "blue", "value": None}]}}, "overrides": []}}
    if decimals is not None:
        p["fieldConfig"]["defaults"]["decimals"] = decimals
    panels.append(p)


def series(title, targets, x, w=12, h=8, unit="none", desc="", maxv=None):
    p = {"type": "timeseries", "title": title, "id": nid(), "datasource": DS, "description": desc,
         "gridPos": {"h": h, "w": w, "x": x, "y": y},
         "targets": [{"refId": chr(65 + i), "datasource": DS, "expr": e, "legendFormat": l} for i, (e, l) in enumerate(targets)],
         "fieldConfig": {"defaults": {"unit": unit, "custom": {"fillOpacity": 10}}, "overrides": []},
         "options": {"legend": {"displayMode": "list", "placement": "bottom"}, "tooltip": {"mode": "multi"}}}
    if maxv is not None:
        p["fieldConfig"]["defaults"]["max"] = maxv
        p["fieldConfig"]["defaults"]["min"] = 0
    panels.append(p)


def table(title, expr, x, w=12, h=8, desc="", hide=()):
    panels.append({"type": "table", "title": title, "id": nid(), "datasource": DS, "description": desc,
                   "gridPos": {"h": h, "w": w, "x": x, "y": y},
                   "targets": [{"refId": "A", "datasource": DS, "expr": expr, "instant": True, "format": "table"}],
                   "transformations": [{"id": "organize", "options": {"excludeByName": {k: True for k in ("Time", *hide)}}}],
                   "fieldConfig": {"defaults": {}, "overrides": []}, "options": {"showHeader": True}})


row("Fleet")
red_below = lambda v: [{"color": "red", "value": None}, {"color": "green", "value": v}]
stat("GPUs reporting", "fleet:gpus:count", 0, desc="GPUs with telemetry right now.")
stat("Allocated to pods", "fleet:gpus_allocated:count", 4)
stat("Unhealthy", "fleet:gpus_unhealthy:count", 8,
     thresholds=[{"color": "green", "value": None}, {"color": "red", "value": 1}])
stat("Healthy capacity", "fleet:gpus_healthy:ratio", 12, unit="percentunit", decimals=1, thresholds=red_below(0.9),
     desc="Healthy, reporting GPUs out of the GPUs expected from the exporter targets.")
stat("Allocated but idle (30m)", "fleet:gpus_allocated_idle:count", 16,
     thresholds=[{"color": "green", "value": None}, {"color": "orange", "value": 1}])
stat("GPU power", "fleet:gpu_power_watts:sum", 20, unit="watt", decimals=0)
y += 4

row("Utilisation")
series("GPU utilisation by node", [("node:gpu_utilization:avg", "{{Hostname}}")], 0, unit="percent", maxv=100)
series("GPU utilisation by namespace (allocated GPUs)", [("namespace:gpu_utilization:avg", "{{namespace}}")], 12,
       unit="percent", maxv=100)
y += 8
series("Per-GPU utilisation ($node)", [('DCGM_FI_DEV_GPU_UTIL{Hostname=~"$node"}', "{{Hostname}} gpu {{gpu}}")], 0, w=24,
       unit="percent", maxv=100)
y += 8

row("Health")
table("Unhealthy GPUs", "gpu:unhealthy", 0, desc="GPUs with hardware XIDs, uncorrectable ECC errors or failed row remapping.",
      hide=("Value", "__name__"))
table("Allocated but idle for 30 minutes", "gpu:allocated_idle:30m", 12, hide=("__name__", "container", "device", "modelName", "pci_bus_id", "UUID"))
y += 8
series("Max GPU temperature by node", [('max by (Hostname) (DCGM_FI_DEV_GPU_TEMP{Hostname=~"$node"})', "{{Hostname}}")], 0,
       unit="celsius")
series("NVLink CRC errors per second", [('sum by (Hostname) (rate(DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL{Hostname=~"$node"}[5m]))', "{{Hostname}}")], 12)
y += 8
table("Firing alerts", 'ALERTS{alertstate="firing"}', 0, w=24, h=6, hide=("Value", "__name__", "alertstate"))
y += 6

row("Power and memory")
series("GPU power by node", [("node:gpu_power_watts:sum", "{{Hostname}}"), ("fleet:gpu_energy_watts:rate5m", "fleet (from energy counter)")], 0, unit="watt")
series("Framebuffer used by namespace", [('sum by (namespace) (DCGM_FI_DEV_FB_USED{pod!=""})', "{{namespace}}")], 12, unit="mbytes")
y += 8

dash = {
    "uid": "gpu-fleet", "title": "GPU fleet", "tags": ["gpu", "dcgm"], "timezone": "utc", "editable": True,
    "schemaVersion": 39, "version": 1, "refresh": "10s", "time": {"from": "now-30m", "to": "now"},
    "templating": {"list": [{
        "name": "node", "label": "Node", "type": "query", "datasource": DS, "includeAll": True, "multi": True,
        "current": {"text": "All", "value": "$__all"}, "allValue": ".*", "refresh": 2,
        "query": {"query": "label_values(DCGM_FI_DEV_GPU_UTIL, Hostname)", "refId": "v"},
        "definition": "label_values(DCGM_FI_DEV_GPU_UTIL, Hostname)"}]},
    "annotations": {"list": []},
    "panels": panels,
}
out = pathlib.Path(__file__).resolve().parent.parent / "grafana" / "dashboards" / "gpu-fleet.json"
out.write_text(json.dumps(dash, indent=2) + "\n")
print(f"wrote {out} ({len(panels)} panels)")
