package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inigogonzalezgarcia/10-gpu-fleet-observability/internal/promapi"
)

// fake answers by the start of the query.
type fake map[string][]promapi.Sample

func (f fake) Query(_ context.Context, q string) ([]promapi.Sample, error) {
	for prefix, s := range f {
		if strings.HasPrefix(q, prefix) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unexpected query %q", q)
}

func s(v float64, kv ...string) promapi.Sample {
	l := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i]] = kv[i+1]
	}
	return promapi.Sample{Labels: l, Value: v}
}

func TestReport(t *testing.T) {
	q := fake{
		`count(DCGM_FI_DEV_GPU_UTIL)`:                                  {s(32)},
		`count(DCGM_FI_DEV_GPU_UTIL{pod!=""})`:                         {s(24)},
		`avg(DCGM_FI_DEV_GPU_UTIL)`:                                    {s(55.2)},
		`avg(DCGM_FI_DEV_GPU_UTIL{pod!=""})`:                           {s(73.6)},
		`sum(DCGM_FI_DEV_POWER_USAGE)`:                                 {s(14200)},
		`count by (namespace)`:                                         {s(16, "namespace", "ml-training"), s(2, "namespace", "research")},
		`avg by (namespace)`:                                           {s(92, "namespace", "ml-training"), s(0, "namespace", "research")},
		`sum by (namespace) (count_over_time`:                          {s(384, "namespace", "ml-training"), s(48, "namespace", "research")},
		`sum by (namespace) (sum_over_time`:                            {s(0, "namespace", "ml-training"), s(48, "namespace", "research")},
		`avg_over_time(`:                                               {s(0, "Hostname", "gpu-node-4", "gpu", "2", "namespace", "research", "pod", "notebook-a")},
		`DCGM_FI_DEV_XID_ERRORS > 0`:                                   {s(79, "Hostname", "gpu-node-2", "gpu", "3"), s(13, "Hostname", "gpu-node-1", "gpu", "0")},
		`DCGM_FI_DEV_ECC_DBE_VOL_TOTAL > 0`:                            {},
		`DCGM_FI_DEV_ROW_REMAP_FAILURE > 0`:                            {s(1, "Hostname", "gpu-node-4", "gpu", "0")},
		`count by (alertname, severity) (ALERTS{alertstate="firing"})`: {s(1, "alertname", "GPUFellOffBus", "severity", "critical")},
	}
	r, err := build(context.Background(), q, "24h", time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if r.GPUs != 32 || r.Allocated != 24 || r.PowerKW != 14.2 {
		t.Fatalf("totals: %+v", r)
	}
	if r.Namespaces[0].Namespace != "ml-training" || r.Namespaces[1].IdleHoursPart != 1 {
		t.Fatalf("namespaces: %+v", r.Namespaces)
	}
	if len(r.Unhealthy) != 2 || r.Unhealthy[0].Reason != "XID 79" || r.Unhealthy[1].Reason != "row remapping failure" {
		t.Fatalf("XID 13 is the application's problem and must not be listed: %+v", r.Unhealthy)
	}
	var b bytes.Buffer
	markdown(&b, r)
	for _, want := range []string{"| 32 | 24 (75%) | 55.2% | 73.6% | 14.2 kW |", "| research | 2 | 0.0% | 48.0 | 48.0 (100%) |",
		"| gpu-node-4 | 2 | research/notebook-a |", "- GPUFellOffBus (critical) x1"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report should contain %q:\n%s", want, b.String())
		}
	}
}
