# Metrics

## From dcgm-exporter (and the simulator)

| Metric | Type | Unit | Used for |
|---|---|---|---|
| `DCGM_FI_DEV_GPU_UTIL` | gauge | % | Utilisation, idle detection, inventory (one series per GPU) |
| `DCGM_FI_DEV_FB_USED` / `DCGM_FI_DEV_FB_FREE` | gauge | MiB | Memory held per namespace |
| `DCGM_FI_DEV_POWER_USAGE` | gauge | W | Power by node and fleet |
| `DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION` | counter | mJ | Energy; `rate()/1000` cross-checks the power gauge |
| `DCGM_FI_DEV_GPU_TEMP` / `DCGM_FI_DEV_MEMORY_TEMP` | gauge | °C | Thermal alerts |
| `DCGM_FI_DEV_SM_CLOCK` | gauge | MHz | Throttling is visible as a clock drop under load |
| `DCGM_FI_DEV_XID_ERRORS` | gauge | XID | Last XID; hardware XIDs alert (see docs/alerts.md) |
| `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL` | gauge | count | Uncorrectable ECC errors since the last reset |
| `DCGM_FI_DEV_ROW_REMAP_FAILURE` | gauge | 0/1 | Memory that cannot be repaired |
| `DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL` | counter | count | NVLink health |

Labels: `gpu`, `UUID`, `pci_bus_id`, `device`, `modelName`, `Hostname`, and `namespace`/`pod`/`container` when the GPU is assigned to a pod (dcgm-exporter adds these from the kubelet's pod-resources API). A GPU without a `pod` label is free.

Which fields a real dcgm-exporter exposes depends on its counters CSV and on the GPU (ECC and row remapping need data-centre GPUs). Check `/metrics` on your exporter before relying on a field.

## Recording rules (`prometheus/rules/recording.yml`)

| Rule | Meaning |
|---|---|
| `fleet:gpus:count` | GPUs reporting |
| `fleet:gpus_expected:count` | 8 × exporter targets, reporting or not |
| `fleet:gpus_allocated:count`, `node:…`, `namespace:…` | GPUs assigned to pods |
| `fleet:gpu_utilization:avg`, `…_allocated:avg`, `node:…`, `namespace:…` | Average utilisation |
| `gpu:allocated_idle:30m`, `fleet:gpus_allocated_idle:count` | Assigned GPUs under 5% for 30 minutes |
| `fleet:gpu_power_watts:sum`, `node:gpu_power_watts:sum` | Power |
| `fleet:gpu_energy_watts:rate5m` | Power derived from the energy counter |
| `gpu:unhealthy`, `fleet:gpus_unhealthy:count` | GPUs that should leave service |
| `fleet:gpus_healthy:ratio` | (reporting − unhealthy) / expected |

The 8 GPUs per node in `fleet:gpus_expected:count` is a lab simplification. With mixed node types, take the expected count from `kube_node_status_capacity{resource="nvidia_com_gpu"}` (kube-state-metrics) instead.
