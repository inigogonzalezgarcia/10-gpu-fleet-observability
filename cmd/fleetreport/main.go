// Command fleetreport asks Prometheus how the GPU fleet is doing and writes a short report in
// Markdown (for a ticket, a chat message or a weekly review) or JSON.
//
//	fleetreport -prometheus http://localhost:9090 -window 24h
//
// It uses raw dcgm-exporter metrics, not this repo's recording rules, so it works against any
// Prometheus that scrapes dcgm-exporter.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/inigogonzalezgarcia/10-gpu-fleet-observability/internal/promapi"
)

type querier interface {
	Query(ctx context.Context, q string) ([]promapi.Sample, error)
}

// Report is everything the command prints.
type Report struct {
	Generated        time.Time        `json:"generated"`
	Window           string           `json:"window"`
	GPUs             int              `json:"gpus"`
	Allocated        int              `json:"allocated"`
	UtilizationAll   float64          `json:"utilizationAllPercent"`
	UtilizationAlloc float64          `json:"utilizationAllocatedPercent"`
	PowerKW          float64          `json:"powerKW"`
	Namespaces       []NamespaceUsage `json:"namespaces"`
	IdleAllocated    []GPURef         `json:"idleAllocated"`
	Unhealthy        []GPURef         `json:"unhealthy"`
	Firing           []string         `json:"firingAlerts"`
}

// NamespaceUsage is GPU use by one team/namespace.
type NamespaceUsage struct {
	Namespace     string  `json:"namespace"`
	GPUs          int     `json:"gpus"`
	Utilization   float64 `json:"utilizationPercent"`
	GPUHours      float64 `json:"gpuHours"`
	IdleGPUHours  float64 `json:"idleGpuHours"`
	IdleHoursPart float64 `json:"idleShare"`
}

// GPURef points at one GPU and says why it is listed.
type GPURef struct {
	Node   string `json:"node"`
	GPU    string `json:"gpu"`
	Owner  string `json:"owner,omitempty"`
	Reason string `json:"reason"`
}

const hardwareXIDs = "48|63|64|74|79|92|95|119|120"

func build(ctx context.Context, q querier, window string, now time.Time) (*Report, error) {
	r := &Report{Generated: now.UTC(), Window: window}
	one := func(expr string) (float64, error) {
		s, err := q.Query(ctx, expr)
		if err != nil || len(s) == 0 {
			return 0, err
		}
		return s[0].Value, nil
	}
	var err error
	var v float64
	if v, err = one(`count(DCGM_FI_DEV_GPU_UTIL)`); err != nil {
		return nil, err
	}
	r.GPUs = int(v)
	if v, err = one(`count(DCGM_FI_DEV_GPU_UTIL{pod!=""})`); err != nil {
		return nil, err
	}
	r.Allocated = int(v)
	if r.UtilizationAll, err = one(`avg(DCGM_FI_DEV_GPU_UTIL)`); err != nil {
		return nil, err
	}
	if r.UtilizationAlloc, err = one(`avg(DCGM_FI_DEV_GPU_UTIL{pod!=""})`); err != nil {
		return nil, err
	}
	if v, err = one(`sum(DCGM_FI_DEV_POWER_USAGE)`); err != nil {
		return nil, err
	}
	r.PowerKW = v / 1000

	// Per namespace: GPUs held now, utilisation, GPU-hours held and GPU-hours held while idle (1-minute steps).
	ns := map[string]*NamespaceUsage{}
	get := func(name string) *NamespaceUsage {
		if ns[name] == nil {
			ns[name] = &NamespaceUsage{Namespace: name}
		}
		return ns[name]
	}
	perNS := []struct {
		expr string
		set  func(*NamespaceUsage, float64)
	}{
		{`count by (namespace) (DCGM_FI_DEV_GPU_UTIL{pod!=""})`, func(u *NamespaceUsage, v float64) { u.GPUs = int(v) }},
		{`avg by (namespace) (DCGM_FI_DEV_GPU_UTIL{pod!=""})`, func(u *NamespaceUsage, v float64) { u.Utilization = v }},
		{fmt.Sprintf(`sum by (namespace) (count_over_time(DCGM_FI_DEV_GPU_UTIL{pod!=""}[%s:1m])) / 60`, window),
			func(u *NamespaceUsage, v float64) { u.GPUHours = v }},
		{fmt.Sprintf(`sum by (namespace) (sum_over_time((DCGM_FI_DEV_GPU_UTIL{pod!=""} < bool 5)[%s:1m])) / 60`, window),
			func(u *NamespaceUsage, v float64) { u.IdleGPUHours = v }},
	}
	for _, p := range perNS {
		s, err := q.Query(ctx, p.expr)
		if err != nil {
			return nil, err
		}
		for _, x := range s {
			p.set(get(x.Labels["namespace"]), x.Value)
		}
	}
	for _, u := range ns {
		if u.GPUHours > 0 {
			u.IdleHoursPart = u.IdleGPUHours / u.GPUHours
		}
		r.Namespaces = append(r.Namespaces, *u)
	}
	sort.Slice(r.Namespaces, func(i, j int) bool {
		if r.Namespaces[i].GPUHours != r.Namespaces[j].GPUHours {
			return r.Namespaces[i].GPUHours > r.Namespaces[j].GPUHours
		}
		return r.Namespaces[i].Namespace < r.Namespaces[j].Namespace
	})

	idle, err := q.Query(ctx, fmt.Sprintf(`avg_over_time(DCGM_FI_DEV_GPU_UTIL{pod!=""}[%s]) < 5`, window))
	if err != nil {
		return nil, err
	}
	for _, s := range idle {
		r.IdleAllocated = append(r.IdleAllocated, GPURef{s.Labels["Hostname"], s.Labels["gpu"],
			s.Labels["namespace"] + "/" + s.Labels["pod"], fmt.Sprintf("%.1f%% average utilisation", s.Value)})
	}

	if r.Unhealthy, err = unhealthy(ctx, q); err != nil {
		return nil, err
	}

	alerts, err := q.Query(ctx, `count by (alertname, severity) (ALERTS{alertstate="firing"})`)
	if err != nil {
		return nil, err
	}
	for _, a := range alerts {
		r.Firing = append(r.Firing, fmt.Sprintf("%s (%s) x%.0f", a.Labels["alertname"], a.Labels["severity"], a.Value))
	}
	sort.Strings(r.Firing)
	sortRefs(r.IdleAllocated)
	return r, nil
}

func unhealthy(ctx context.Context, q querier) ([]GPURef, error) {
	var out []GPURef
	checks := []struct{ expr, format string }{
		{`DCGM_FI_DEV_XID_ERRORS > 0`, "XID %.0f"},
		{`DCGM_FI_DEV_ECC_DBE_VOL_TOTAL > 0`, "%.0f uncorrectable ECC error(s)"},
		{`DCGM_FI_DEV_ROW_REMAP_FAILURE > 0`, "row remapping failure"},
	}
	hw := map[string]bool{}
	for _, x := range strings.Split(hardwareXIDs, "|") {
		hw[x] = true
	}
	for _, c := range checks {
		s, err := q.Query(ctx, c.expr)
		if err != nil {
			return nil, err
		}
		for _, x := range s {
			if strings.HasPrefix(c.format, "XID") && !hw[fmt.Sprintf("%.0f", x.Value)] {
				continue // application XIDs are the workload's problem, not the GPU's
			}
			reason := c.format
			if strings.Contains(c.format, "%") {
				reason = fmt.Sprintf(c.format, x.Value)
			}
			out = append(out, GPURef{Node: x.Labels["Hostname"], GPU: x.Labels["gpu"], Reason: reason})
		}
	}
	sortRefs(out)
	return out, nil
}

func sortRefs(r []GPURef) {
	sort.Slice(r, func(i, j int) bool {
		if r[i].Node != r[j].Node {
			return r[i].Node < r[j].Node
		}
		return r[i].GPU < r[j].GPU
	})
}

func markdown(w io.Writer, r *Report) {
	fmt.Fprintf(w, "# GPU fleet report\n\nGenerated %s, window %s.\n\n", r.Generated.Format("2006-01-02 15:04 UTC"), r.Window)
	fmt.Fprintf(w, "| GPUs | Allocated | Utilisation (all) | Utilisation (allocated) | Power |\n|---|---|---|---|---|\n")
	fmt.Fprintf(w, "| %d | %d (%.0f%%) | %.1f%% | %.1f%% | %.1f kW |\n\n", r.GPUs, r.Allocated, pct(r.Allocated, r.GPUs),
		r.UtilizationAll, r.UtilizationAlloc, r.PowerKW)

	fmt.Fprintf(w, "## Usage by namespace\n\n| Namespace | GPUs now | Utilisation | GPU-hours | Idle GPU-hours |\n|---|---|---|---|---|\n")
	for _, n := range r.Namespaces {
		fmt.Fprintf(w, "| %s | %d | %.1f%% | %.1f | %.1f (%.0f%%) |\n", n.Namespace, n.GPUs, n.Utilization, n.GPUHours, n.IdleGPUHours, n.IdleHoursPart*100)
	}

	fmt.Fprintf(w, "\n## Allocated but idle\n\n")
	if len(r.IdleAllocated) == 0 {
		fmt.Fprintln(w, "None.")
	} else {
		fmt.Fprintf(w, "| Node | GPU | Held by | Why |\n|---|---|---|---|\n")
		for _, g := range r.IdleAllocated {
			fmt.Fprintf(w, "| %s | %s | %s | %s |\n", g.Node, g.GPU, g.Owner, g.Reason)
		}
	}

	fmt.Fprintf(w, "\n## Unhealthy GPUs\n\n")
	if len(r.Unhealthy) == 0 {
		fmt.Fprintln(w, "None.")
	} else {
		fmt.Fprintf(w, "| Node | GPU | Why |\n|---|---|---|\n")
		for _, g := range r.Unhealthy {
			fmt.Fprintf(w, "| %s | %s | %s |\n", g.Node, g.GPU, g.Reason)
		}
	}

	fmt.Fprintf(w, "\n## Firing alerts\n\n")
	if len(r.Firing) == 0 {
		fmt.Fprintln(w, "None.")
	}
	for _, a := range r.Firing {
		fmt.Fprintf(w, "- %s\n", a)
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func main() {
	prom := flag.String("prometheus", "http://localhost:9090", "Prometheus base URL")
	window := flag.String("window", "24h", "look-back window (Prometheus duration)")
	format := flag.String("format", "md", "md or json")
	flag.Parse()
	r, err := build(context.Background(), promapi.New(strings.TrimRight(*prom, "/")), *window, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return
	}
	markdown(os.Stdout, r)
}
