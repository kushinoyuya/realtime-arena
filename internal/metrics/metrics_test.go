package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestCounterAndGauge(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("arena_requests_total", "total requests")
	g := r.NewGauge("arena_connections", "live connections")

	c.Inc()
	c.Add(4)
	g.Set(3)
	g.Inc()
	g.Dec()

	if c.Value() != 5 {
		t.Fatalf("counter = %d, want 5", c.Value())
	}
	if g.Value() != 3 {
		t.Fatalf("gauge = %d, want 3", g.Value())
	}

	var b bytes.Buffer
	if err := r.WriteProm(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE arena_requests_total counter",
		"arena_requests_total 5",
		"# TYPE arena_connections gauge",
		"arena_connections 3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exposition missing %q in:\n%s", want, out)
		}
	}
}

func TestHistogramExposition(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("arena_tick_seconds", "tick duration",
		[]float64{0.001, 0.005, 0.01})

	// Observations: two in <=0.001, one in <=0.005, one in <=0.01, one over.
	h.Observe(0.0005)
	h.Observe(0.0008)
	h.Observe(0.003)
	h.Observe(0.009)
	h.Observe(0.05)

	var b bytes.Buffer
	if err := r.WriteProm(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	// Cumulative buckets: le=0.001 -> 2, le=0.005 -> 3, le=0.01 -> 4, +Inf -> 5.
	for _, want := range []string{
		`arena_tick_seconds_bucket{le="0.001"} 2`,
		`arena_tick_seconds_bucket{le="0.005"} 3`,
		`arena_tick_seconds_bucket{le="0.01"} 4`,
		`arena_tick_seconds_bucket{le="+Inf"} 5`,
		"arena_tick_seconds_count 5",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exposition missing %q in:\n%s", want, out)
		}
	}
}

func TestBoundsAreSorted(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("h", "h", []float64{0.01, 0.001, 0.1})
	h.Observe(0.005) // <= 0.01 and <= 0.1 but not <= 0.001
	var b bytes.Buffer
	_ = r.WriteProm(&b)
	out := b.String()
	if !strings.Contains(out, `h_bucket{le="0.001"} 0`) ||
		!strings.Contains(out, `h_bucket{le="0.01"} 1`) ||
		!strings.Contains(out, `h_bucket{le="0.1"} 1`) {
		t.Fatalf("bounds not sorted / miscounted:\n%s", out)
	}
}
