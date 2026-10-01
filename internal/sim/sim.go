// Package sim simulates a small GPU fleet that looks, to Prometheus, like a set of nodes running
// NVIDIA dcgm-exporter: same metric names, same labels (Hostname, gpu, UUID, modelName, and
// namespace/pod/container when a GPU is assigned to a pod).
//
// Workloads follow simple profiles so dashboards and recording rules have something realistic to
// show, and faults can be injected at runtime to exercise alerting. Nothing here touches hardware.
package sim

import (
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// Profile is what a GPU is doing.
type Profile string

const (
	Free      Profile = "free"      // not assigned to any pod
	Training  Profile = "training"  // high, steady utilisation
	Inference Profile = "inference" // moderate, follows a daily-ish wave
	Stuck     Profile = "stuck"     // assigned to a pod, memory held, no work: paying for nothing
)

// FBTotalMiB is the framebuffer size reported for the simulated 80 GB GPU.
const FBTotalMiB = 81559

// GPU is one simulated device.
type GPU struct {
	Index     int
	UUID      string
	BusID     string
	Profile   Profile
	Namespace string
	Pod       string

	// Faults.
	XID          int
	DBE          float64
	RemapFailure bool
	TempOffset   float64 // added to the normal temperature
	NVLinkErrPS  float64 // NVLink CRC flit errors per second

	// Counters (monotonic).
	energyMJ    float64
	nvlinkErrs  float64
	lastAdvance time.Time
}

// Node is one simulated host with its exporter.
type Node struct {
	Name         string
	GPUs         []*GPU
	ExporterDown bool
}

// Fleet is the whole simulation.
type Fleet struct {
	mu    sync.Mutex
	Nodes []*Node
	rng   *rand.Rand
	now   func() time.Time
	start time.Time
}

// New builds a fleet of n nodes with g GPUs each, all free.
func New(n, g int, seed int64) *Fleet {
	f := &Fleet{rng: rand.New(rand.NewSource(seed)), now: time.Now}
	f.start = f.now()
	for i := 1; i <= n; i++ {
		node := &Node{Name: fmt.Sprintf("gpu-node-%d", i)}
		for j := 0; j < g; j++ {
			node.GPUs = append(node.GPUs, &GPU{
				Index:   j,
				UUID:    fmt.Sprintf("GPU-%08x-%04x-%04x-%04x-%012x", uint32(i*1000+j), i, j, 0xbeef, int64(i)<<16|int64(j)),
				BusID:   fmt.Sprintf("00000000:%02X:00.0", 0x18+j*0x10),
				Profile: Free, lastAdvance: f.start,
			})
		}
		f.Nodes = append(f.Nodes, node)
	}
	return f
}

// Default returns the lab fleet: four 8-GPU nodes with a mix of training, inference, idle
// and one forgotten notebook holding two GPUs.
func Default(seed int64) *Fleet {
	f := New(4, 8, seed)
	for _, g := range f.Nodes[0].GPUs {
		g.Profile, g.Namespace, g.Pod = Training, "ml-training", "llm-pretrain-worker-0"
	}
	for _, g := range f.Nodes[1].GPUs {
		g.Profile, g.Namespace, g.Pod = Training, "ml-training", "llm-pretrain-worker-1"
	}
	for j, g := range f.Nodes[2].GPUs[:4] {
		g.Profile, g.Namespace, g.Pod = Inference, "serving", fmt.Sprintf("chat-api-%d", j)
	}
	for j, g := range f.Nodes[3].GPUs[:2] {
		g.Profile, g.Namespace, g.Pod = Inference, "serving", fmt.Sprintf("embeddings-%d", j)
	}
	for _, g := range f.Nodes[3].GPUs[2:4] {
		g.Profile, g.Namespace, g.Pod = Stuck, "research", "notebook-a-7f9c"
	}
	return f
}

// SetClock replaces the clock (tests).
func (f *Fleet) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
	f.start = now()
	for _, n := range f.Nodes {
		for _, g := range n.GPUs {
			g.lastAdvance = f.start
		}
	}
}

// Node returns a node by name.
func (f *Fleet) Node(name string) (*Node, bool) {
	for _, n := range f.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return nil, false
}

// Lock and Unlock guard changes made by the control API.
func (f *Fleet) Lock()   { f.mu.Lock() }
func (f *Fleet) Unlock() { f.mu.Unlock() }

func (f *Fleet) jitter(spread float64) float64 { return (f.rng.Float64()*2 - 1) * spread }

// sample computes the instantaneous gauges of a GPU.
type sample struct {
	util, fbUsed, power, temp, memTemp, smClock float64
}

func (f *Fleet) sample(g *GPU, t time.Time) sample {
	var s sample
	switch g.Profile {
	case Training:
		s.util = clamp(92+f.jitter(5), 0, 100)
		s.fbUsed = 71000 + f.jitter(1500)
	case Inference:
		wave := math.Sin(float64(t.Unix()%86400) / 86400 * 2 * math.Pi)
		s.util = clamp(45+20*wave+f.jitter(6), 0, 100)
		s.fbUsed = 31000 + f.jitter(800)
	case Stuck:
		s.util = 0
		s.fbUsed = 42000
	default:
		s.util = 0
		s.fbUsed = 1
	}
	if g.XID == 79 { // fell off the bus: no work gets through
		s.util = 0
	}
	s.power = 68 + s.util*6.2 + f.jitter(4)
	s.temp = 31 + s.util*0.45 + g.TempOffset + f.jitter(1)
	s.memTemp = s.temp + 6
	s.smClock = 345
	if s.util > 1 {
		s.smClock = 1980
		if s.temp >= 87 { // thermal slowdown
			s.smClock = 1410
		}
	}
	return s
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// advance moves the counters of a GPU forward to t.
func (g *GPU) advance(t time.Time, powerW float64) {
	dt := t.Sub(g.lastAdvance).Seconds()
	if dt <= 0 {
		return
	}
	g.energyMJ += powerW * dt * 1000 // W·s = J; DCGM reports mJ
	g.nvlinkErrs += g.NVLinkErrPS * dt
	g.lastAdvance = t
}

type field struct{ name, typ, help string }

var fields = []field{
	{"DCGM_FI_DEV_GPU_UTIL", "gauge", "GPU utilization (in %)."},
	{"DCGM_FI_DEV_FB_USED", "gauge", "Framebuffer memory used (in MiB)."},
	{"DCGM_FI_DEV_FB_FREE", "gauge", "Framebuffer memory free (in MiB)."},
	{"DCGM_FI_DEV_POWER_USAGE", "gauge", "Power draw (in W)."},
	{"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "counter", "Total energy consumption since boot (in mJ)."},
	{"DCGM_FI_DEV_GPU_TEMP", "gauge", "GPU temperature (in C)."},
	{"DCGM_FI_DEV_MEMORY_TEMP", "gauge", "Memory temperature (in C)."},
	{"DCGM_FI_DEV_SM_CLOCK", "gauge", "SM clock frequency (in MHz)."},
	{"DCGM_FI_DEV_XID_ERRORS", "gauge", "Value of the last XID error encountered."},
	{"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", "gauge", "Total number of double-bit volatile ECC errors."},
	{"DCGM_FI_DEV_ROW_REMAP_FAILURE", "gauge", "Whether remapping of rows has failed."},
	{"DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL", "counter", "Total number of NVLink flow-control CRC errors."},
}

// ErrExporterDown is returned when a node's exporter is switched off.
var ErrExporterDown = fmt.Errorf("exporter down")

// WriteMetrics writes the exposition page for one node.
func (f *Fleet) WriteMetrics(w io.Writer, nodeName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.Node(nodeName)
	if !ok {
		return fmt.Errorf("unknown node %q", nodeName)
	}
	if n.ExporterDown {
		return ErrExporterDown
	}
	t := f.now()
	samples := make([]sample, len(n.GPUs))
	for i, g := range n.GPUs {
		samples[i] = f.sample(g, t)
		g.advance(t, samples[i].power)
	}
	var b strings.Builder
	for _, fd := range fields {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", fd.name, fd.help, fd.name, fd.typ)
		for i, g := range n.GPUs {
			s := samples[i]
			var v float64
			switch fd.name {
			case "DCGM_FI_DEV_GPU_UTIL":
				v = math.Round(s.util)
			case "DCGM_FI_DEV_FB_USED":
				v = math.Round(s.fbUsed)
			case "DCGM_FI_DEV_FB_FREE":
				v = math.Round(FBTotalMiB - s.fbUsed)
			case "DCGM_FI_DEV_POWER_USAGE":
				v = math.Round(s.power*100) / 100
			case "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION":
				v = math.Floor(g.energyMJ)
			case "DCGM_FI_DEV_GPU_TEMP":
				v = math.Round(s.temp)
			case "DCGM_FI_DEV_MEMORY_TEMP":
				v = math.Round(s.memTemp)
			case "DCGM_FI_DEV_SM_CLOCK":
				v = s.smClock
			case "DCGM_FI_DEV_XID_ERRORS":
				v = float64(g.XID)
			case "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL":
				v = g.DBE
			case "DCGM_FI_DEV_ROW_REMAP_FAILURE":
				if g.RemapFailure {
					v = 1
				}
			case "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL":
				v = math.Floor(g.nvlinkErrs)
			}
			fmt.Fprintf(&b, "%s{%s} %g\n", fd.name, labels(n, g), v)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func labels(n *Node, g *GPU) string {
	l := []string{
		fmt.Sprintf(`gpu="%d"`, g.Index),
		fmt.Sprintf(`UUID="%s"`, g.UUID),
		fmt.Sprintf(`pci_bus_id="%s"`, g.BusID),
		fmt.Sprintf(`device="nvidia%d"`, g.Index),
		`modelName="NVIDIA H100 80GB HBM3"`,
		fmt.Sprintf(`Hostname="%s"`, n.Name),
	}
	if g.Pod != "" {
		l = append(l, `container="main"`, fmt.Sprintf(`namespace="%s"`, g.Namespace), fmt.Sprintf(`pod="%s"`, g.Pod))
	}
	return strings.Join(l, ",")
}

// Reset clears all faults and restores the default workloads.
func (f *Fleet) Reset(seed int64) {
	fresh := Default(seed)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, n := range f.Nodes {
		n.ExporterDown = false
		for j, g := range n.GPUs {
			d := fresh.Nodes[i].GPUs[j]
			g.Profile, g.Namespace, g.Pod = d.Profile, d.Namespace, d.Pod
			g.XID, g.DBE, g.RemapFailure, g.TempOffset, g.NVLinkErrPS = 0, 0, false, 0, 0
		}
	}
}

// GPUState is the JSON view of a GPU for the control API.
type GPUState struct {
	Node         string  `json:"node"`
	GPU          int     `json:"gpu"`
	Profile      Profile `json:"profile"`
	Namespace    string  `json:"namespace,omitempty"`
	Pod          string  `json:"pod,omitempty"`
	XID          int     `json:"xid,omitempty"`
	DBE          float64 `json:"dbe,omitempty"`
	RemapFailure bool    `json:"remapFailure,omitempty"`
	TempOffset   float64 `json:"tempOffset,omitempty"`
	NVLinkErrPS  float64 `json:"nvlinkErrorsPerSecond,omitempty"`
	ExporterDown bool    `json:"exporterDown,omitempty"`
}

// State lists every GPU.
func (f *Fleet) State() []GPUState {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []GPUState
	for _, n := range f.Nodes {
		for _, g := range n.GPUs {
			out = append(out, GPUState{n.Name, g.Index, g.Profile, g.Namespace, g.Pod, g.XID, g.DBE,
				g.RemapFailure, g.TempOffset, g.NVLinkErrPS, n.ExporterDown})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}
