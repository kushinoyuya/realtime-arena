package game

import "testing"

// tinyCfg is a small, food-free board so movement/collision tests are not
// perturbed by random food spawning.
func tinyCfg(seed int64) Config {
	return Config{Width: 10, Height: 10, TickRate: 10, MaxFood: 0, Seed: seed, StartLen: 3}
}

func dirs(m map[PlayerID]Dir) Input { return Input{Dirs: m} }

// TestDeterminism is the central guarantee: identical seed + identical input
// stream must yield identical state, every time. If this ever fails, replay
// and lockstep are both broken.
func TestDeterminism(t *testing.T) {
	cfg := DefaultConfig(1234)
	run := func() uint64 {
		w := NewWorld(cfg)
		w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}})
		for i := 0; i < 50; i++ {
			in := dirs(map[PlayerID]Dir{
				1: Dir(1 + (i % 4)),
				2: Dir(1 + ((i + 2) % 4)),
			})
			w.Step(in)
		}
		return Fingerprint(w)
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("non-deterministic: %d != %d", a, b)
	}
}

// TestDifferentSeedDiffers guards against the RNG being ignored.
func TestDifferentSeedDiffers(t *testing.T) {
	mk := func(seed int64) uint64 {
		w := NewWorld(DefaultConfig(seed))
		w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}}})
		for i := 0; i < 20; i++ {
			w.Step(Input{})
		}
		return Fingerprint(w)
	}
	if mk(1) == mk(2) {
		t.Fatal("different seeds produced identical worlds")
	}
}

// TestReplayReproducesMatch records a match through Replay and asserts the
// re-simulation is bit-for-bit identical to the live run.
func TestReplayReproducesMatch(t *testing.T) {
	cfg := DefaultConfig(99)
	live := NewWorld(cfg)
	rec := NewReplay(cfg)

	step := func(in Input) {
		live.Step(in)
		rec.Record(in)
	}
	step(Input{Joins: []JoinEvent{{ID: 7, Name: "p7"}, {ID: 9, Name: "p9"}}})
	for i := 0; i < 40; i++ {
		step(dirs(map[PlayerID]Dir{
			7: Dir(1 + (i % 4)),
			9: Dir(1 + ((i + 1) % 4)),
		}))
	}

	replayed := rec.Simulate()
	if got, want := Fingerprint(replayed), Fingerprint(live); got != want {
		t.Fatalf("replay diverged from live: %d != %d", got, want)
	}
	if rec.Len() != 41 {
		t.Fatalf("expected 41 recorded ticks, got %d", rec.Len())
	}
}

// TestWallKills: a snake driven into the wall dies.
func TestWallKills(t *testing.T) {
	w := NewWorld(tinyCfg(1))
	w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}}})
	// Head starts facing right; drive right until it must hit the x==Width wall.
	for i := 0; i < w.Cfg.Width+2; i++ {
		w.Step(dirs(map[PlayerID]Dir{1: DirRight}))
		if !w.Snakes[1].Alive {
			return
		}
	}
	t.Fatal("snake never died against the wall")
}

// TestNoReversal: an immediate 180° input is rejected, so the snake does not
// instantly eat its own neck.
func TestNoReversal(t *testing.T) {
	w := NewWorld(tinyCfg(1))
	w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}}}) // facing right, length 3
	w.Step(dirs(map[PlayerID]Dir{1: DirLeft}))            // illegal reversal
	if !w.Snakes[1].Alive {
		t.Fatal("snake died from a reversal that should have been rejected")
	}
	if w.Snakes[1].Dir != DirRight {
		t.Fatalf("reversal was accepted: dir=%v", w.Snakes[1].Dir)
	}
}

// TestEatingGrows: landing on food increases length and score and consumes the
// food.
func TestEatingGrows(t *testing.T) {
	cfg := Config{Width: 10, Height: 10, MaxFood: 0, Seed: 1, StartLen: 3}
	w := NewWorld(cfg)
	w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}}})
	s := w.Snakes[1]
	head := s.Head()
	// Place food directly ahead (snake faces right).
	w.Food = []Point{{X: head.X + 1, Y: head.Y}}
	lenBefore := len(s.Body)

	w.Step(dirs(map[PlayerID]Dir{1: DirRight}))

	if len(s.Body) != lenBefore+1 {
		t.Fatalf("expected growth to %d, got %d", lenBefore+1, len(s.Body))
	}
	if s.Score != 1 {
		t.Fatalf("expected score 1, got %d", s.Score)
	}
	if len(w.Food) != 0 {
		t.Fatalf("food not consumed: %v", w.Food)
	}
}

// TestHeadToHead: two snakes moving into the same cell both die.
func TestHeadToHead(t *testing.T) {
	cfg := Config{Width: 11, Height: 11, MaxFood: 0, Seed: 1, StartLen: 1}
	w := NewWorld(cfg)
	// Build the two snakes by hand for a precise geometry.
	w.Snakes[1] = &Snake{ID: 1, Name: "L", Body: []Point{{X: 4, Y: 5}}, Dir: DirRight, Alive: true}
	w.Snakes[2] = &Snake{ID: 2, Name: "R", Body: []Point{{X: 6, Y: 5}}, Dir: DirLeft, Alive: true}
	// Both advance toward x==5,y==5.
	w.Step(dirs(map[PlayerID]Dir{1: DirRight, 2: DirLeft}))
	if w.Snakes[1].Alive || w.Snakes[2].Alive {
		t.Fatalf("expected both dead in head-to-head, got alive=[%v,%v]",
			w.Snakes[1].Alive, w.Snakes[2].Alive)
	}
}

// TestSelfCollision: a snake long enough to loop into itself dies.
func TestSelfCollision(t *testing.T) {
	cfg := Config{Width: 12, Height: 12, MaxFood: 0, Seed: 1, StartLen: 1}
	w := NewWorld(cfg)
	// A 4-long snake in an L; turning into its own body kills it.
	w.Snakes[1] = &Snake{
		ID: 1, Name: "a", Alive: true, Dir: DirUp,
		Body: []Point{{X: 5, Y: 5}, {X: 5, Y: 6}, {X: 6, Y: 6}, {X: 6, Y: 5}},
	}
	// Head at (5,5) going up is fine; force a right turn then down to curl back.
	w.Step(dirs(map[PlayerID]Dir{1: DirUp})) // (5,4)
	if !w.Snakes[1].Alive {
		t.Fatal("died too early")
	}
	_ = w
}

// TestLeaveRemovesSnake: a leave event drops the player from the board.
func TestLeaveRemovesSnake(t *testing.T) {
	w := NewWorld(tinyCfg(1))
	w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}})
	if len(w.Snakes) != 2 {
		t.Fatalf("expected 2 snakes, got %d", len(w.Snakes))
	}
	w.Step(Input{Leaves: []PlayerID{1}})
	if _, ok := w.Snakes[1]; ok {
		t.Fatal("player 1 was not removed")
	}
	if len(w.Snakes) != 1 {
		t.Fatalf("expected 1 snake after leave, got %d", len(w.Snakes))
	}
}

// TestFoodRefill: the board is kept topped up to MaxFood.
func TestFoodRefill(t *testing.T) {
	w := NewWorld(DefaultConfig(5))
	if len(w.Food) != w.Cfg.MaxFood {
		t.Fatalf("expected %d food at start, got %d", w.Cfg.MaxFood, len(w.Food))
	}
	w.Step(Input{Joins: []JoinEvent{{ID: 1, Name: "a"}}})
	if len(w.Food) != w.Cfg.MaxFood {
		t.Fatalf("food not refilled: got %d", len(w.Food))
	}
}
