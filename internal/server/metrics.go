package server

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/kushino/realtime-arena/internal/metrics"
	"github.com/kushino/realtime-arena/internal/slo"
)

// slowTickSeconds is the tick-latency budget. A tick that takes longer than this
// counts against the latency SLO below. The sim tick is sub-millisecond under
// normal load, so crossing 50 ms means the server is in trouble.
const slowTickSeconds = 0.050

// appMetrics is the arena's instrumentation. Handles are created once at
// startup and updated from the hot paths; the registry renders them for /metrics.
type appMetrics struct {
	reg *metrics.Registry

	conns       *metrics.Gauge
	rooms       *metrics.Gauge
	players     *metrics.Gauge
	snapshots   *metrics.Counter
	inputs      *metrics.Counter
	joins       *metrics.Counter
	disconnects *metrics.Counter
	ticksTotal  *metrics.Counter
	slowTicks   *metrics.Counter
	tickDur     *metrics.Histogram
}

func newAppMetrics() *appMetrics {
	r := metrics.NewRegistry()
	return &appMetrics{
		reg:         r,
		conns:       r.NewGauge("arena_ws_connections", "current open websocket connections"),
		rooms:       r.NewGauge("arena_active_rooms", "current number of match rooms"),
		players:     r.NewGauge("arena_active_players", "current number of players across all rooms"),
		snapshots:   r.NewCounter("arena_snapshots_sent_total", "snapshots fanned out to clients"),
		inputs:      r.NewCounter("arena_inputs_total", "direction inputs accepted from clients"),
		joins:       r.NewCounter("arena_joins_total", "player joins"),
		disconnects: r.NewCounter("arena_disconnects_total", "client disconnects"),
		ticksTotal:  r.NewCounter("arena_ticks_total", "simulation ticks processed"),
		slowTicks:   r.NewCounter("arena_slow_ticks_total", "ticks slower than the latency budget"),
		tickDur: r.NewHistogram("arena_tick_duration_seconds", "per-tick simulation + broadcast time",
			[]float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25}),
	}
}

// observeTick records one tick's duration and whether it broke the budget.
func (m *appMetrics) observeTick(seconds float64) {
	if m == nil {
		return
	}
	m.tickDur.Observe(seconds)
	m.ticksTotal.Inc()
	if seconds > slowTickSeconds {
		m.slowTicks.Inc()
	}
}

// tickLatencySLO is the objective the /slo endpoint reports against.
var tickLatencySLO = slo.SLO{Name: "tick latency < 50ms", Target: 0.999}

// ServeMetrics renders Prometheus exposition. Gauges that are cheapest to read
// at scrape time (room/player counts) are refreshed here.
func (h *Hub) ServeMetrics(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	rooms := append([]*Room(nil), h.rooms...)
	h.mu.Unlock()

	players := 0
	for _, rm := range rooms {
		players += int(atomic.LoadInt32(&rm.count))
	}
	h.metrics.rooms.Set(int64(len(rooms)))
	h.metrics.players.Set(int64(players))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_ = h.metrics.reg.WriteProm(w)
}

// ServeSLO reports the live tick-latency SLO: how much error budget remains and
// the current burn rate, computed from the process-lifetime tick counters.
func (h *Hub) ServeSLO(w http.ResponseWriter, r *http.Request) {
	win := slo.Window{
		Total: h.metrics.ticksTotal.Value(),
		Bad:   h.metrics.slowTicks.Value(),
	}
	resp := struct {
		Objective       string  `json:"objective"`
		Target          float64 `json:"target"`
		ErrorBudget     float64 `json:"errorBudget"`
		Ticks           uint64  `json:"ticks"`
		SlowTicks       uint64  `json:"slowTicks"`
		FailureRatio    float64 `json:"failureRatio"`
		BudgetRemaining float64 `json:"budgetRemaining"`
		BurnRate        float64 `json:"burnRate"`
	}{
		Objective:       tickLatencySLO.Name,
		Target:          tickLatencySLO.Target,
		ErrorBudget:     tickLatencySLO.ErrorBudget(),
		Ticks:           win.Total,
		SlowTicks:       win.Bad,
		FailureRatio:    win.FailureRatio(),
		BudgetRemaining: tickLatencySLO.BudgetRemaining(win),
		BurnRate:        tickLatencySLO.BurnRate(win),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
