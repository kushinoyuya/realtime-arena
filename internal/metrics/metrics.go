// Package metrics is a tiny, dependency-free metrics registry that exposes
// counters, gauges and histograms in the Prometheus text exposition format.
// A real service would pull in prometheus/client_golang; this project keeps its
// zero-dependency property and, in doing so, shows the exposition format is
// understood rather than imported. The output is scrapeable by Prometheus,
// Grafana Agent, the OTel Collector, etc.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing integer metric (requests, errors …).
type Counter struct {
	name string
	help string
	v    uint64
}

// Inc adds one.
func (c *Counter) Inc() { atomic.AddUint64(&c.v, 1) }

// Add adds n (n must be >= 0 for counter semantics).
func (c *Counter) Add(n uint64) { atomic.AddUint64(&c.v, n) }

// Value returns the current count.
func (c *Counter) Value() uint64 { return atomic.LoadUint64(&c.v) }

// Gauge is a value that can go up and down (connections, queue depth …).
type Gauge struct {
	name string
	help string
	v    int64
}

// Set replaces the value.
func (g *Gauge) Set(n int64) { atomic.StoreInt64(&g.v, n) }

// Inc / Dec move the gauge by one.
func (g *Gauge) Inc() { atomic.AddInt64(&g.v, 1) }
func (g *Gauge) Dec() { atomic.AddInt64(&g.v, -1) }

// Value returns the current value.
func (g *Gauge) Value() int64 { return atomic.LoadInt64(&g.v) }

// Histogram buckets observations into cumulative "less-than-or-equal" buckets,
// matching Prometheus histogram semantics.
type Histogram struct {
	name    string
	help    string
	mu      sync.Mutex
	bounds  []float64 // upper bounds, ascending
	buckets []uint64  // cumulative counts, len == len(bounds)
	sum     float64
	count   uint64
}

// Observe records one sample.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	h.sum += v
	h.count++
	for i, b := range h.bounds {
		if v <= b {
			h.buckets[i]++
		}
	}
	h.mu.Unlock()
}

// Registry owns a set of metrics and renders them. Registration is expected at
// startup; rendering is safe to call concurrently with metric updates.
type Registry struct {
	mu         sync.Mutex
	counters   []*Counter
	gauges     []*Gauge
	histograms []*Histogram
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// NewCounter registers and returns a counter.
func (r *Registry) NewCounter(name, help string) *Counter {
	c := &Counter{name: name, help: help}
	r.mu.Lock()
	r.counters = append(r.counters, c)
	r.mu.Unlock()
	return c
}

// NewGauge registers and returns a gauge.
func (r *Registry) NewGauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	r.mu.Lock()
	r.gauges = append(r.gauges, g)
	r.mu.Unlock()
	return g
}

// NewHistogram registers and returns a histogram with the given upper bounds
// (seconds, bytes, …). Bounds are sorted ascending; a +Inf bucket is implicit.
func (r *Registry) NewHistogram(name, help string, bounds []float64) *Histogram {
	b := append([]float64(nil), bounds...)
	sort.Float64s(b)
	h := &Histogram{
		name:    name,
		help:    help,
		bounds:  b,
		buckets: make([]uint64, len(b)),
	}
	r.mu.Lock()
	r.histograms = append(r.histograms, h)
	r.mu.Unlock()
	return h
}

// WriteProm renders every metric in Prometheus text exposition format.
func (r *Registry) WriteProm(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, c := range r.counters {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
			c.name, c.help, c.name, c.name, c.Value()); err != nil {
			return err
		}
	}
	for _, g := range r.gauges {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n",
			g.name, g.help, g.name, g.name, g.Value()); err != nil {
			return err
		}
	}
	for _, h := range r.histograms {
		h.mu.Lock()
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
		for i, b := range h.bounds {
			fmt.Fprintf(w, "%s_bucket{le=\"%g\"} %d\n", h.name, b, h.buckets[i])
		}
		fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.count)
		fmt.Fprintf(w, "%s_sum %g\n", h.name, h.sum)
		fmt.Fprintf(w, "%s_count %d\n", h.name, h.count)
		h.mu.Unlock()
	}
	return nil
}
