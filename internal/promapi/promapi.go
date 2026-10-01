// Package promapi is a small client for the Prometheus HTTP query API (instant queries only).
package promapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Sample is one element of an instant vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Client queries one Prometheus server.
type Client struct {
	Base string
	HTTP *http.Client
}

// New returns a client with a timeout.
func New(base string) *Client {
	return &Client{Base: base, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type response struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// Query runs an instant query and returns the vector (a scalar comes back as one unlabelled sample).
func (c *Client) Query(ctx context.Context, q string) ([]Sample, error) {
	u := c.Base + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("query %q: %s: %w", q, resp.Status, err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("query %q: %s: %s", q, r.ErrorType, r.Error)
	}
	switch r.Data.ResultType {
	case "vector":
		var raw []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(r.Data.Result, &raw); err != nil {
			return nil, err
		}
		out := make([]Sample, 0, len(raw))
		for _, s := range raw {
			v, err := value(s.Value[1])
			if err != nil {
				return nil, err
			}
			out = append(out, Sample{Labels: s.Metric, Value: v})
		}
		return out, nil
	case "scalar":
		var raw [2]any
		if err := json.Unmarshal(r.Data.Result, &raw); err != nil {
			return nil, err
		}
		v, err := value(raw[1])
		if err != nil {
			return nil, err
		}
		return []Sample{{Labels: map[string]string{}, Value: v}}, nil
	default:
		return nil, fmt.Errorf("query %q: unsupported result type %q", q, r.Data.ResultType)
	}
}

func value(v any) (float64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("unexpected value %v", v)
	}
	return strconv.ParseFloat(s, 64)
}

// Scalar runs a query expected to return one number; ok is false when the result is empty.
func (c *Client) Scalar(ctx context.Context, q string) (v float64, ok bool, err error) {
	s, err := c.Query(ctx, q)
	if err != nil || len(s) == 0 {
		return 0, false, err
	}
	return s[0].Value, true, nil
}
