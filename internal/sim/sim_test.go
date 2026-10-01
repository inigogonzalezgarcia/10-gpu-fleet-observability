package sim

import (
	"bufio"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// values returns metric -> list of (labels, value) for a node page.
func values(t *testing.T, f *Fleet, node string) map[string][]struct {
	labels string
	v      float64
} {
	t.Helper()
	var b strings.Builder
	if err := f.WriteMetrics(&b, node); err != nil {
		t.Fatal(err)
	}
	out := map[string][]struct {
		labels string
		v      float64
	}{}
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		i, j := strings.IndexByte(line, '{'), strings.LastIndexByte(line, '}')
		v, err := strconv.ParseFloat(strings.TrimSpace(line[j+1:]), 64)
		if i < 0 || err != nil {
			t.Fatalf("bad line %q", line)
		}
		out[line[:i]] = append(out[line[:i]], struct {
			labels string
			v      float64
		}{line[i+1 : j], v})
	}
	return out
}

func TestDefaultFleetShape(t *testing.T) {
	f := Default(1)
	if len(f.Nodes) != 4 || len(f.Nodes[0].GPUs) != 8 {
		t.Fatalf("want 4x8, got %dx%d", len(f.Nodes), len(f.Nodes[0].GPUs))
	}
	m := values(t, f, "gpu-node-1")
	if len(m["DCGM_FI_DEV_GPU_UTIL"]) != 8 || len(m) != len(fields) {
		t.Fatalf("want 8 GPUs and %d fields, got %d / %d", len(fields), len(m["DCGM_FI_DEV_GPU_UTIL"]), len(m))
	}
	for _, s := range m["DCGM_FI_DEV_GPU_UTIL"] {
		if s.v < 80 || !strings.Contains(s.labels, `pod="llm-pretrain-worker-0"`) {
			t.Fatalf("node 1 should be training: %+v", s)
		}
	}
}

func TestStuckAndFreeGPUs(t *testing.T) {
	m := values(t, Default(1), "gpu-node-4")
	util := m["DCGM_FI_DEV_GPU_UTIL"]
	fb := m["DCGM_FI_DEV_FB_USED"]
	if util[2].v != 0 || !strings.Contains(util[2].labels, `namespace="research"`) || fb[2].v < 40000 {
		t.Fatalf("GPU 2 should be allocated, idle and holding memory: %+v %+v", util[2], fb[2])
	}
	if strings.Contains(util[6].labels, "pod=") {
		t.Fatalf("free GPUs carry no pod labels: %s", util[6].labels)
	}
}

func TestCountersAreMonotonic(t *testing.T) {
	f := Default(1)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	f.Nodes[0].GPUs[0].NVLinkErrPS = 2
	first := values(t, f, "gpu-node-1")
	now = now.Add(10 * time.Second)
	second := values(t, f, "gpu-node-1")
	e1, e2 := first["DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION"][0].v, second["DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION"][0].v
	// ~600 W for 10 s = ~6,000,000 mJ
	if e2-e1 < 4e6 || e2-e1 > 8e6 {
		t.Fatalf("energy should grow by ~6e6 mJ in 10 s, got %v", e2-e1)
	}
	if n := second["DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL"][0].v; n != 20 {
		t.Fatalf("want 20 NVLink errors after 10 s at 2/s, got %v", n)
	}
}

func TestFaultsShowUp(t *testing.T) {
	f := Default(1)
	g := f.Nodes[1].GPUs[5]
	g.XID, g.DBE, g.RemapFailure, g.TempOffset = 79, 2, true, 40
	m := values(t, f, "gpu-node-2")
	if m["DCGM_FI_DEV_XID_ERRORS"][5].v != 79 || m["DCGM_FI_DEV_ECC_DBE_VOL_TOTAL"][5].v != 2 ||
		m["DCGM_FI_DEV_ROW_REMAP_FAILURE"][5].v != 1 {
		t.Fatal("faults should be visible in the metrics")
	}
	if m["DCGM_FI_DEV_GPU_UTIL"][5].v != 0 {
		t.Fatal("a GPU that fell off the bus does no work")
	}
}

func TestExporterDownAndReset(t *testing.T) {
	f := Default(1)
	f.Nodes[2].ExporterDown = true
	f.Nodes[0].GPUs[0].XID = 79
	var b strings.Builder
	if err := f.WriteMetrics(&b, "gpu-node-3"); !errors.Is(err, ErrExporterDown) {
		t.Fatalf("want ErrExporterDown, got %v", err)
	}
	f.Reset(1)
	if f.Nodes[2].ExporterDown || f.Nodes[0].GPUs[0].XID != 0 {
		t.Fatal("reset should clear faults")
	}
	if err := f.WriteMetrics(&b, "nope"); err == nil {
		t.Fatal("unknown node should be an error")
	}
}
