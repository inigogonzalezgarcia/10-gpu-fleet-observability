// Command fleet-sim serves a simulated GPU fleet: one dcgm-exporter-style /metrics endpoint per
// node (ports base+1, base+2, ...) and a control API on the base port to inject faults.
//
//	GET  :9400/state
//	POST :9400/fault?node=gpu-node-2&gpu=3&xid=79      (also dbe=2, remap=1, temp=+30, nvlink=5)
//	POST :9400/workload?node=gpu-node-4&gpu=0&profile=stuck&namespace=research&pod=nb-1
//	POST :9400/exporter?node=gpu-node-3&down=1
//	POST :9400/reset
//	GET  :9401/metrics  (gpu-node-1), :9402/metrics (gpu-node-2), ...
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/inigogonzalezgarcia/10-gpu-fleet-observability/internal/sim"
)

func main() {
	base := flag.Int("port", 9400, "control API port; node N is served on port+N")
	seed := flag.Int64("seed", 42, "random seed")
	flag.Parse()

	f := sim.Default(*seed)
	for i, n := range f.Nodes {
		addr := fmt.Sprintf(":%d", *base+i+1)
		go serve(addr, nodeHandler(f, n.Name))
		slog.Info("node exporter", "node", n.Name, "addr", addr)
	}
	slog.Info("control API", "addr", fmt.Sprintf(":%d", *base))
	if err := serve(fmt.Sprintf(":%d", *base), controlHandler(f, *seed)); err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
}

func serve(addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}

func nodeHandler(f *sim.Fleet, node string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if err := f.WriteMetrics(w, node); err != nil {
			if errors.Is(err, sim.ErrExporterDown) {
				http.Error(w, "exporter down (simulated)", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return mux
}

func controlHandler(f *sim.Fleet, seed int64) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.State())
	})
	mux.HandleFunc("/reset", post(func(w http.ResponseWriter, _ *http.Request) error {
		f.Reset(seed)
		return nil
	}))
	mux.HandleFunc("/exporter", post(func(w http.ResponseWriter, r *http.Request) error {
		f.Lock()
		defer f.Unlock()
		n, ok := f.Node(r.URL.Query().Get("node"))
		if !ok {
			return fmt.Errorf("unknown node")
		}
		n.ExporterDown = r.URL.Query().Get("down") == "1"
		return nil
	}))
	mux.HandleFunc("/fault", post(func(w http.ResponseWriter, r *http.Request) error {
		f.Lock()
		defer f.Unlock()
		g, err := gpuFrom(f, r)
		if err != nil {
			return err
		}
		q := r.URL.Query()
		for key, set := range map[string]func(float64){
			"xid":    func(v float64) { g.XID = int(v) },
			"dbe":    func(v float64) { g.DBE = v },
			"remap":  func(v float64) { g.RemapFailure = v > 0 },
			"temp":   func(v float64) { g.TempOffset = v },
			"nvlink": func(v float64) { g.NVLinkErrPS = v },
		} {
			if s := q.Get(key); s != "" {
				v, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				set(v)
			}
		}
		return nil
	}))
	mux.HandleFunc("/workload", post(func(w http.ResponseWriter, r *http.Request) error {
		f.Lock()
		defer f.Unlock()
		g, err := gpuFrom(f, r)
		if err != nil {
			return err
		}
		q := r.URL.Query()
		p := sim.Profile(q.Get("profile"))
		switch p {
		case sim.Free:
			g.Profile, g.Namespace, g.Pod = p, "", ""
		case sim.Training, sim.Inference, sim.Stuck:
			if q.Get("namespace") == "" || q.Get("pod") == "" {
				return fmt.Errorf("namespace and pod are required")
			}
			g.Profile, g.Namespace, g.Pod = p, q.Get("namespace"), q.Get("pod")
		default:
			return fmt.Errorf("profile must be free, training, inference or stuck")
		}
		return nil
	}))
	return mux
}

func gpuFrom(f *sim.Fleet, r *http.Request) (*sim.GPU, error) {
	n, ok := f.Node(r.URL.Query().Get("node"))
	if !ok {
		return nil, fmt.Errorf("unknown node")
	}
	i, err := strconv.Atoi(r.URL.Query().Get("gpu"))
	if err != nil || i < 0 || i >= len(n.GPUs) {
		return nil, fmt.Errorf("gpu must be 0..%d", len(n.GPUs)-1)
	}
	return n.GPUs[i], nil
}

func post(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if err := h(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Info("control", "path", r.URL.Path, "query", r.URL.RawQuery)
		fmt.Fprintln(w, "ok")
	}
}
