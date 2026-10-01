package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSinkRecordsByReceiver(t *testing.T) {
	s := &sink{keep: 2}
	body := `{"receiver":"pager","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"GPUFellOffBus","Hostname":"gpu-node-2","gpu":"3"}}]}`
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/pager", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("post: %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/received", nil))
	var got []Received
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Receiver != "pager" || got[0].Labels["alertname"] != "GPUFellOffBus" {
		t.Fatalf("unexpected: %+v", got)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/pager", strings.NewReader("{")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON should be rejected, got %d", w.Code)
	}
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/received", nil))
	if len(s.got) != 0 {
		t.Fatal("DELETE should clear")
	}
}
