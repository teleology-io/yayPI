// Package metrics is a small, dependency-free Prometheus exposition: labelled counters,
// histograms and gauge callbacks rendered in the text format (version 0.0.4) that any
// Prometheus-compatible scraper (Prometheus, Grafana Agent, Datadog, OTel collector)
// understands.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry holds metric families.
type Registry struct {
	mu       sync.Mutex
	families []family
}

type family interface {
	name() string
	write(w io.Writer)
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) add(f family) {
	r.mu.Lock()
	r.families = append(r.families, f)
	r.mu.Unlock()
}

// Handler serves the registry in Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.Render(w)
	})
}

// Render writes every family, sorted by name.
func (r *Registry) Render(w io.Writer) {
	r.mu.Lock()
	fams := append([]family(nil), r.families...)
	r.mu.Unlock()
	sort.Slice(fams, func(i, j int) bool { return fams[i].name() < fams[j].name() })
	for _, f := range fams {
		f.write(w)
	}
}

// labelKey joins label values into a map key.
func labelKey(values []string) string { return strings.Join(values, "\xff") }

func formatLabels(names, values []string, extra ...string) string {
	if len(names) == 0 && len(extra) == 0 {
		return ""
	}
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		parts = append(parts, n+`="`+escapeLabel(values[i])+`"`)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, extra[i]+`="`+extra[i+1]+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func formatFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ── Counter ───────────────────────────────────────────────────────────────────

// Counter is a monotonically increasing, labelled counter.
type Counter struct {
	n, help string
	labels  []string
	mu      sync.Mutex
	vals    map[string]*atomic.Uint64
	lv      map[string][]string
}

// NewCounter registers a counter.
func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	c := &Counter{n: name, help: help, labels: labels, vals: map[string]*atomic.Uint64{}, lv: map[string][]string{}}
	r.add(c)
	return c
}

// Inc adds 1 for the given label values.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add adds n for the given label values.
func (c *Counter) Add(n uint64, values ...string) {
	k := labelKey(values)
	c.mu.Lock()
	v, ok := c.vals[k]
	if !ok {
		v = &atomic.Uint64{}
		c.vals[k] = v
		c.lv[k] = append([]string(nil), values...)
	}
	c.mu.Unlock()
	v.Add(n)
}

func (c *Counter) name() string { return c.n }

func (c *Counter) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.n, c.help, c.n)
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.vals))
	for k := range c.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s%s %d\n", c.n, formatLabels(c.labels, c.lv[k]), c.vals[k].Load())
	}
}

// ── Histogram ─────────────────────────────────────────────────────────────────

// DefaultBuckets suits HTTP latencies in seconds.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Histogram is a labelled histogram.
type Histogram struct {
	n, help string
	labels  []string
	buckets []float64
	mu      sync.Mutex
	series  map[string]*histSeries
}

type histSeries struct {
	values []string
	counts []uint64
	sum    float64
	count  uint64
}

// NewHistogram registers a histogram (nil buckets = DefaultBuckets).
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) *Histogram {
	if buckets == nil {
		buckets = DefaultBuckets
	}
	h := &Histogram{n: name, help: help, labels: labels, buckets: buckets, series: map[string]*histSeries{}}
	r.add(h)
	return h
}

// Observe records v for the given label values.
func (h *Histogram) Observe(v float64, values ...string) {
	k := labelKey(values)
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.series[k]
	if !ok {
		s = &histSeries{values: append([]string(nil), values...), counts: make([]uint64, len(h.buckets))}
		h.series[k] = s
	}
	for i, b := range h.buckets {
		if v <= b {
			s.counts[i]++
		}
	}
	s.sum += v
	s.count++
}

func (h *Histogram) name() string { return h.n }

func (h *Histogram) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.n, h.help, h.n)
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.series))
	for k := range h.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := h.series[k]
		for i, b := range h.buckets {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.n, formatLabels(h.labels, s.values, "le", formatFloat(b)), s.counts[i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.n, formatLabels(h.labels, s.values, "le", "+Inf"), s.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.n, formatLabels(h.labels, s.values), formatFloat(s.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.n, formatLabels(h.labels, s.values), s.count)
	}
}

// ── Gauge ─────────────────────────────────────────────────────────────────────

// GaugeFunc is a gauge whose labelled values are read at scrape time.
type GaugeFunc struct {
	n, help string
	labels  []string
	fn      func() map[string]float64 // label values joined by "\xff" → value
}

// NewGaugeFunc registers a gauge sampled by fn at scrape time. fn returns a map from
// label-value tuples (use LabelValues) to values.
func (r *Registry) NewGaugeFunc(name, help string, fn func() map[string]float64, labels ...string) {
	r.add(&GaugeFunc{n: name, help: help, labels: labels, fn: fn})
}

// LabelValues builds the key GaugeFunc callbacks return.
func LabelValues(values ...string) string { return labelKey(values) }

func (g *GaugeFunc) name() string { return g.n }

func (g *GaugeFunc) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.n, g.help, g.n)
	vals := g.fn()
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var lv []string
		if len(g.labels) > 0 {
			lv = strings.Split(k, "\xff")
		}
		fmt.Fprintf(w, "%s%s %s\n", g.n, formatLabels(g.labels, lv), formatFloat(vals[k]))
	}
}
