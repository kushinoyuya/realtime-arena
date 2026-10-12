package game

import (
	"hash/fnv"
	"sort"
	"strconv"
)

// Replay is a complete, self-contained record of a match: the ruleset plus the
// per-tick input events. Because Step is pure, re-feeding Inputs to a fresh
// World reproduces the match exactly — the basis for replays, regression tests
// and (in a real product) server-side verification of client-reported results.
type Replay struct {
	Cfg    Config  `json:"cfg"`
	Inputs []Input `json:"inputs"`
}

// NewReplay starts an empty recording for the given config.
func NewReplay(cfg Config) *Replay {
	return &Replay{Cfg: cfg}
}

// Record appends the input applied on one tick.
func (r *Replay) Record(in Input) {
	r.Inputs = append(r.Inputs, in)
}

// Len is the number of recorded ticks.
func (r *Replay) Len() int { return len(r.Inputs) }

// Simulate replays every recorded tick into a fresh World and returns the final
// state. The result is deterministic: equal Replays always yield equal Worlds.
func (r *Replay) Simulate() *World {
	w := NewWorld(r.Cfg)
	for _, in := range r.Inputs {
		w.Step(in)
	}
	return w
}

// SimulateTo replays the first n ticks and returns the intermediate World.
func (r *Replay) SimulateTo(n int) *World {
	w := NewWorld(r.Cfg)
	for i := 0; i < n && i < len(r.Inputs); i++ {
		w.Step(r.Inputs[i])
	}
	return w
}

// Fingerprint is a stable 64-bit digest of a World's observable state. Two
// Worlds with the same fingerprint are equal for gameplay purposes. It is used
// by tests to assert that replays and re-simulations match without having to
// deep-compare slices by hand.
func Fingerprint(w *World) uint64 {
	h := fnv.New64a()
	write := func(s string) { _, _ = h.Write([]byte(s)) }
	writeInt := func(n int) { write(strconv.Itoa(n)); write(";") }

	writeInt(w.Tick)
	writeInt(w.Cfg.Width)
	writeInt(w.Cfg.Height)

	for _, id := range w.sortedIDs() {
		s := w.Snakes[id]
		write("S")
		writeInt(int(id))
		if s.Alive {
			write("a")
		} else {
			write("d")
		}
		writeInt(s.Score)
		for _, p := range s.Body {
			write("(")
			writeInt(p.X)
			writeInt(p.Y)
			write(")")
		}
	}

	// Food order is not gameplay-significant, so sort before hashing.
	food := make([]Point, len(w.Food))
	copy(food, w.Food)
	sort.Slice(food, func(i, j int) bool {
		if food[i].Y != food[j].Y {
			return food[i].Y < food[j].Y
		}
		return food[i].X < food[j].X
	})
	write("F")
	for _, p := range food {
		writeInt(p.X)
		writeInt(p.Y)
	}
	return h.Sum64()
}
