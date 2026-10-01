# Alert runbooks

One section per alert in `prometheus/rules/alerts.yml`; each alert links here through `runbook_url`.
Severities: **critical** pages (or triggers automation such as repo 09's remediation controller), **warning** opens a ticket, **info** goes to the team that owns the workload.

## GPUFellOffBus

**Fires:** `DCGM_FI_DEV_XID_ERRORS == 79`, immediately. **Routes to:** pager. **Label:** `action=drain`.

The driver lost contact with the GPU. Jobs using it hang or crash, and the GPU reports 0% utilisation.

1. Check the kernel log on the node: `journalctl -k | grep -i xid`. Several GPUs at once points to the PCIe switch, the board or power, not a single GPU.
2. Drain the node (or let the remediation controller do it), then reset the GPU (`nvidia-smi -r -i <gpu>`) or reboot.
3. Validate before returning the node to service (DCGM diagnostics, `dcgmi diag -r 3`).
4. A second XID 79 on the same GPU soon after: quarantine and open a hardware ticket.

While it fires, `GPUAllocatedButIdle` for the same GPU is inhibited: the pod is not wasting the GPU, the GPU is broken.

## GPUHardwareXID

**Fires:** XID 48, 63, 74, 95, 119 or 120, immediately. **Routes to:** pager. **Label:** `action=drain`.

Errors NVIDIA lists with hardware or driver causes that need a GPU reset (double-bit ECC, pending row remap, NVLink, uncontained memory error, GSP firmware). Same steps as GPUFellOffBus. XID 63 is expected after ECC errors: the reset applies the pending row remap.

## GPUUncorrectableECC

**Fires:** `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL > 0`. **Routes to:** pager. **Label:** `action=drain`.

Data in GPU memory was corrupted and could not be corrected. Results computed on this GPU since the error may be wrong: tell the job owner. Drain, reset (which clears the volatile counter and applies row remapping), validate.

## GPUMemoryUnrepairable

**Fires:** row remapping failed, or XID 64 / 92. **Routes to:** pager. **Label:** `action=quarantine`.

The GPU has run out of spare memory rows, or single-bit errors are frequent enough to predict failure. Repairing in place will not hold. Quarantine the node, open a hardware ticket (RMA) with the GPU UUID and serial, and do not return it to service after a reset.

## NVLinkErrorsIncreasing

**Fires:** more than 1 NVLink CRC error per second for 10 minutes. **Routes to:** ticket.

Multi-GPU jobs on the node will slow down (retransmissions) and can fail. Check whether the errors are on one link or all of them (`nvidia-smi nvlink -e`). One link: reseat or replace hardware at the next maintenance window. All links on the node: firmware or the NVSwitch tray.

## GPUTemperatureHigh

**Fires:** 85°C or more for 5 minutes. **Routes to:** ticket.

Early warning. Check whether the whole node or rack is warm (cooling) or a single GPU (fan, heatsink, blocked airflow). Compare with neighbours on the dashboard's temperature panel.

## GPUTemperatureCritical

**Fires:** 90°C or more for 2 minutes. **Routes to:** pager. **Label:** `action=cordon`.

The GPU is throttling clocks, so jobs slow down. Cordon the node: running work continues, new work goes elsewhere. Uncordon once it has stayed cool. Inhibits GPUTemperatureHigh for the same GPU.

## GPUExporterDown

**Fires:** the exporter target has been down for 1 minute. **Routes to:** pager.

No telemetry means every other alert for that node is blind, including the hardware ones. Check the dcgm-exporter pod on the node, the DCGM host engine, and whether the node itself is up. A node that is down shows up here first.

## GPUFleetHealthyCapacityLow

**Fires:** less than 90% of expected GPUs are healthy and reporting, for 5 minutes. **Routes to:** pager.

Many GPUs failing at once usually has one cause: a driver or firmware rollout, a rack losing power or cooling, a network partition hiding exporters. Find what the affected nodes have in common before handling them one by one, and pause automated remediation if it would drain a large part of the fleet.

## GPUAllocatedButIdle

**Fires:** a GPU assigned to a pod has averaged under 5% utilisation for 30 minutes. **Routes to:** the owning team (grouped by namespace).

The GPU is reserved, so nobody else can schedule on it. Typical causes: a notebook left open, a job waiting on data or on another job, an inference service sized for peak traffic. Not an outage, a cost: the team decides whether to scale down, add an idle timeout, or accept it.
