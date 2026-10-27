package game

import "sort"

// rng is a small, fully deterministic PRNG (xorshift64*). We deliberately do
// NOT use math/rand: we need bit-for-bit identical results across machines and
// Go versions so that a replay re-simulates exactly. Seeding goes through
// splitmix64 so even low-entropy seeds (0, 1, 2 …) produce well-mixed streams.
type rng struct{ state uint64 }

func newRNG(seed int64) *rng {
	// splitmix64 to initialise the state from the seed.
	z := uint64(seed) + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z = z ^ (z >> 31)
	if z == 0 {
		z = 0x9e3779b97f4a7c15 // xorshift must never have an all-zero state
	}
	return &rng{state: z}
}

func (r *rng) next() uint64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 0x2545f4914f6cdd1d
}

// intn returns a value in [0, n). n must be > 0.
func (r *rng) intn(n int) int {
	return int(r.next() % uint64(n))
}

// JoinEvent records a player entering the arena on a given tick.
type JoinEvent struct {
	ID   PlayerID `json:"id"`
	Name string   `json:"name"`
}

// Input is everything that can change the world on a single tick. A whole
// match is therefore (Config.Seed, []Input): feed the same slice to a fresh
// World and you get the same game back. This event-sourced shape is what makes
// replay trivially correct.
type Input struct {
	Joins  []JoinEvent      `json:"joins,omitempty"`
	Leaves []PlayerID       `json:"leaves,omitempty"`
	Dirs   map[PlayerID]Dir `json:"dirs,omitempty"`
}

// World is the authoritative match state.
type World struct {
	Cfg    Config
	Tick   int
	Snakes map[PlayerID]*Snake
	Food   []Point
	rng    *rng
}

// NewWorld creates an empty arena and spawns the initial food.
func NewWorld(cfg Config) *World {
	w := &World{
		Cfg:    cfg,
		Snakes: make(map[PlayerID]*Snake),
		rng:    newRNG(cfg.Seed),
	}
	w.refillFood()
	return w
}

// sortedIDs returns the live player IDs in ascending order. Iterating a Go map
// directly is randomised per run, which would destroy determinism, so every
// loop that affects state goes through this.
func (w *World) sortedIDs() []PlayerID {
	ids := make([]PlayerID, 0, len(w.Snakes))
	for id := range w.Snakes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Step advances the simulation by exactly one tick. It is pure with respect to
// (current state, in): no clocks, no global RNG, no map-order dependence.
func (w *World) Step(in Input) {
	w.applyJoins(in.Joins)
	w.applyLeaves(in.Leaves)
	w.applyDirs(in.Dirs)
	w.move()
	w.refillFood()
	w.Tick++
}

func (w *World) applyJoins(joins []JoinEvent) {
	for _, j := range joins {
		if _, exists := w.Snakes[j.ID]; exists {
			continue
		}
		w.spawnSnake(j.ID, j.Name)
	}
}

func (w *World) applyLeaves(leaves []PlayerID) {
	for _, id := range leaves {
		delete(w.Snakes, id)
	}
}

// applyDirs validates and records each player's requested direction. Invalid
// requests (unknown player, 180° reversal into the neck) are silently dropped
// — this is the simplest form of server-side input validation / anti-cheat.
func (w *World) applyDirs(dirs map[PlayerID]Dir) {
	if dirs == nil {
		return
	}
	for _, id := range w.sortedIDs() {
		d, ok := dirs[id]
		if !ok || d == DirNone {
			continue
		}
		s := w.Snakes[id]
		if !s.Alive {
			continue
		}
		if len(s.Body) > 1 && opposite(d, s.Dir) {
			continue // cannot reverse into own neck
		}
		s.Dir = d
	}
}

// move advances every living snake one cell and resolves all collisions
// simultaneously, which is what makes head-to-head outcomes fair.
func (w *World) move() {
	ids := w.sortedIDs()

	// 1. Move heads; kill anything that leaves the board. Eating is detected
	//    against the food set captured before any movement.
	ate := make(map[PlayerID]int) // snake -> index into w.Food it consumed
	for _, id := range ids {
		s := w.Snakes[id]
		if !s.Alive {
			continue
		}
		dx, dy := s.Dir.delta()
		if dx == 0 && dy == 0 {
			continue // not yet moving (fresh snake with no input)
		}
		head := s.Head()
		nh := Point{X: head.X + dx, Y: head.Y + dy}
		if nh.X < 0 || nh.Y < 0 || nh.X >= w.Cfg.Width || nh.Y >= w.Cfg.Height {
			s.Alive = false
			continue
		}
		if fi := w.foodIndex(nh); fi >= 0 {
			ate[id] = fi
		}
		s.Body = append([]Point{nh}, s.Body...)
		if _, grew := ate[id]; !grew {
			s.Body = s.Body[:len(s.Body)-1] // no food: drop the tail
		}
	}

	// Remove eaten food (descending indices so earlier removals don't shift
	// the ones still to delete), and bump scores. Two heads can land on the
	// same food in one tick, so each index is removed only once — removing it
	// twice would delete an uneaten food or, at the last index, panic.
	eaten := make([]int, 0, len(ate))
	removed := make(map[int]bool, len(ate))
	for _, id := range ids {
		if fi, ok := ate[id]; ok {
			if !removed[fi] {
				removed[fi] = true
				eaten = append(eaten, fi)
			}
			w.Snakes[id].Score++
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(eaten)))
	for _, fi := range eaten {
		w.Food = append(w.Food[:fi], w.Food[fi+1:]...)
	}

	// 2. Build occupancy of every living body cell, then kill any snake whose
	//    head shares a cell with another body cell. occ[head] > 1 covers
	//    self-collision, running into another body, and head-to-head (both
	//    heads land on the same cell, so both die).
	occ := make(map[Point]int)
	for _, id := range ids {
		s := w.Snakes[id]
		if !s.Alive {
			continue
		}
		for _, p := range s.Body {
			occ[p]++
		}
	}
	for _, id := range ids {
		s := w.Snakes[id]
		if !s.Alive {
			continue
		}
		if occ[s.Head()] > 1 {
			s.Alive = false
		}
	}
}

// spawnSnake places a new snake of StartLen cells on a free row, facing right.
func (w *World) spawnSnake(id PlayerID, name string) {
	start := w.Cfg.StartLen
	if start < 1 {
		start = 1
	}
	var head Point
	placed := false
	for attempt := 0; attempt < 200 && !placed; attempt++ {
		hx := start + w.rng.intn(max(1, w.Cfg.Width-start-1))
		hy := 1 + w.rng.intn(max(1, w.Cfg.Height-2))
		ok := true
		for i := 0; i < start; i++ {
			if w.occupied(Point{X: hx - i, Y: hy}) {
				ok = false
				break
			}
		}
		if ok {
			head = Point{X: hx, Y: hy}
			placed = true
		}
	}
	body := make([]Point, start)
	for i := 0; i < start; i++ {
		body[i] = Point{X: head.X - i, Y: head.Y}
	}
	w.Snakes[id] = &Snake{
		ID:    id,
		Name:  name,
		Body:  body,
		Dir:   DirRight,
		Alive: true,
		Score: 0,
	}
}

// refillFood tops the board up to Config.MaxFood, choosing empty cells with the
// deterministic RNG.
func (w *World) refillFood() {
	for len(w.Food) < w.Cfg.MaxFood {
		free := w.freeCellCount()
		if free == 0 {
			return
		}
		p, ok := w.randomFreeCell()
		if !ok {
			return
		}
		w.Food = append(w.Food, p)
	}
}

func (w *World) randomFreeCell() (Point, bool) {
	// Rejection sampling, bounded so a nearly-full board can't spin forever.
	for attempt := 0; attempt < 500; attempt++ {
		p := Point{X: w.rng.intn(w.Cfg.Width), Y: w.rng.intn(w.Cfg.Height)}
		if !w.occupied(p) {
			return p, true
		}
	}
	// Deterministic fallback: first free cell in scan order.
	for y := 0; y < w.Cfg.Height; y++ {
		for x := 0; x < w.Cfg.Width; x++ {
			p := Point{X: x, Y: y}
			if !w.occupied(p) {
				return p, true
			}
		}
	}
	return Point{}, false
}

func (w *World) occupied(p Point) bool {
	if w.foodIndex(p) >= 0 {
		return true
	}
	for _, id := range w.sortedIDs() {
		for _, b := range w.Snakes[id].Body {
			if b == p {
				return true
			}
		}
	}
	return false
}

func (w *World) foodIndex(p Point) int {
	for i, f := range w.Food {
		if f == p {
			return i
		}
	}
	return -1
}

func (w *World) freeCellCount() int {
	used := len(w.Food)
	for _, id := range w.sortedIDs() {
		used += len(w.Snakes[id].Body)
	}
	return w.Cfg.Width*w.Cfg.Height - used
}

// AliveCount returns how many snakes are still in play.
func (w *World) AliveCount() int {
	n := 0
	for _, id := range w.sortedIDs() {
		if w.Snakes[id].Alive {
			n++
		}
	}
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
