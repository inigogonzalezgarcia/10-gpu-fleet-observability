// Command alert-sink stands in for the paging, ticketing and chat systems in the lab. It accepts
// Alertmanager webhooks on /<receiver> and lists what it got on GET /received, so tests can check
// routing and inhibition end to end.
package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Received is one alert as delivered to one receiver.
type Received struct {
	Receiver string            `json:"receiver"`
	Status   string            `json:"status"`
	Labels   map[string]string `json:"labels"`
	At       time.Time         `json:"at"`
}

type sink struct {
	mu   sync.Mutex
	got  []Received
	keep int
}

// webhook is the part of the Alertmanager webhook payload we use.
type webhook struct {
	Receiver string `json:"receiver"`
	Alerts   []struct {
		Status string            `json:"status"`
		Labels map[string]string `json:"labels"`
	} `json:"alerts"`
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/received":
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.got)
	case r.Method == http.MethodDelete && r.URL.Path == "/received":
		s.mu.Lock()
		s.got = nil
		s.mu.Unlock()
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		_, _ = w.Write([]byte("ok\n"))
	case r.Method == http.MethodPost:
		var p webhook
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		path := strings.Trim(r.URL.Path, "/")
		s.mu.Lock()
		for _, a := range p.Alerts {
			s.got = append(s.got, Received{Receiver: path, Status: a.Status, Labels: a.Labels, At: time.Now().UTC()})
			slog.Info("alert", "receiver", path, "status", a.Status, "alertname", a.Labels["alertname"],
				"node", a.Labels["Hostname"], "gpu", a.Labels["gpu"])
		}
		if len(s.got) > s.keep {
			s.got = s.got[len(s.got)-s.keep:]
		}
		s.mu.Unlock()
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func main() {
	listen := flag.String("listen", ":9099", "address to listen on")
	flag.Parse()
	srv := &http.Server{Addr: *listen, Handler: &sink{keep: 1000}, ReadHeaderTimeout: 5 * time.Second}
	slog.Info("alert-sink listening", "addr", *listen)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
}
