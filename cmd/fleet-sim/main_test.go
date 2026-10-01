package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inigogonzalezgarcia/10-gpu-fleet-observability/internal/sim"
)

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestControlAPI(t *testing.T) {
	f := sim.Default(1)
	c := controlHandler(f, 1)
	cases := []struct {
		method, target string
		code           int
	}{
		{"POST", "/fault?node=gpu-node-2&gpu=3&xid=79", 200},
		{"POST", "/fault?node=gpu-node-2&gpu=9&xid=79", 400},
		{"POST", "/fault?node=nope&gpu=0&xid=79", 400},
		{"POST", "/fault?node=gpu-node-1&gpu=0&temp=abc", 400},
		{"GET", "/fault?node=gpu-node-2&gpu=3&xid=79", 405},
		{"POST", "/workload?node=gpu-node-4&gpu=6&profile=stuck&namespace=research&pod=nb-2", 200},
		{"POST", "/workload?node=gpu-node-4&gpu=6&profile=stuck", 400},
		{"POST", "/workload?node=gpu-node-4&gpu=6&profile=gaming", 400},
		{"POST", "/exporter?node=gpu-node-3&down=1", 200},
	}
	for _, tc := range cases {
		if w := do(c, tc.method, tc.target); w.Code != tc.code {
			t.Errorf("%s %s: want %d, got %d (%s)", tc.method, tc.target, tc.code, w.Code, w.Body.String())
		}
	}
	if f.Nodes[1].GPUs[3].XID != 79 || f.Nodes[3].GPUs[6].Pod != "nb-2" || !f.Nodes[2].ExporterDown {
		t.Fatal("control calls should change the fleet")
	}
	if w := do(nodeHandler(f, "gpu-node-3"), "GET", "/metrics"); w.Code != 503 {
		t.Fatalf("a down exporter should answer 503, got %d", w.Code)
	}
	if w := do(nodeHandler(f, "gpu-node-2"), "GET", "/metrics"); w.Code != 200 || !strings.Contains(w.Body.String(), `DCGM_FI_DEV_XID_ERRORS{gpu="3"`) {
		t.Fatalf("node page: %d", w.Code)
	}
	do(c, "POST", "/reset")
	if w := do(c, "GET", "/state"); !strings.Contains(w.Body.String(), `"node":"gpu-node-4"`) || strings.Contains(w.Body.String(), `"xid":79`) {
		t.Fatalf("state after reset: %s", w.Body.String())
	}
}
