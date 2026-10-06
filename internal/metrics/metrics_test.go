package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("http_requests_total", "Requests.", "method", "status")
	c.Inc("GET", "200")
	c.Inc("GET", "200")
	h := r.NewHistogram("latency_seconds", "Latency.", []float64{0.1, 1}, "route")
	h.Observe(0.05, "/x")
	h.Observe(0.5, "/x")
	r.NewGaugeFunc("db_open", "Open conns.", func() map[string]float64 { return map[string]float64{LabelValues("primary"): 3} }, "db")

	var buf bytes.Buffer
	r.Render(&buf)
	out := buf.String()
	for _, want := range []string{
		`http_requests_total{method="GET",status="200"} 2`,
		`latency_seconds_bucket{route="/x",le="0.1"} 1`,
		`latency_seconds_bucket{route="/x",le="+Inf"} 2`,
		`latency_seconds_count{route="/x"} 2`,
		`db_open{db="primary"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
