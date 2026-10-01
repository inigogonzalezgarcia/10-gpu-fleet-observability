package promapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("query") {
		case "vec":
			io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"Hostname":"gpu-node-1"},"value":[1700000000,"91.5"]}]}}`)
		case "sc":
			io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"4"]}}`)
		case "empty":
			io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		default:
			w.WriteHeader(400)
			io.WriteString(w, `{"status":"error","errorType":"bad_data","error":"parse error"}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	s, err := c.Query(ctx, "vec")
	if err != nil || len(s) != 1 || s[0].Value != 91.5 || s[0].Labels["Hostname"] != "gpu-node-1" {
		t.Fatalf("vector: %v %+v", err, s)
	}
	if v, ok, err := c.Scalar(ctx, "sc"); err != nil || !ok || v != 4 {
		t.Fatalf("scalar: %v %v %v", v, ok, err)
	}
	if _, ok, err := c.Scalar(ctx, "empty"); ok || err != nil {
		t.Fatalf("empty: %v %v", ok, err)
	}
	if _, err := c.Query(ctx, "bad"); err == nil {
		t.Fatal("API errors should surface")
	}
}
