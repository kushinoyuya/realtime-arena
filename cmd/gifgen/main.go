// Command gifgen renders a demo match to an animated GIF using the real game
// engine (internal/game). It drives a handful of greedy food-seeking bots,
// steps the deterministic simulation, and paints each tick to a frame. Because
// the simulation is deterministic, the same seed always produces the same GIF —
// handy for a stable README asset. It uses only the standard library.
//
//	go run ./cmd/gifgen -out docs/demo.gif
package main

import (
	"flag"
	"image"
	"image/color"
	"image/gif"
	"log"
	"os"
	"sort"

	"github.com/kushino/realtime-arena/internal/game"
)

// palette index layout: 0 bg, 1 grid, 2 food, 3 dead, 4.. snake colours.
var palette = color.Palette{
	rgb(0x1a, 0x22, 0x2c), // bg
	rgb(0x24, 0x30, 0x41), // grid
	rgb(0xff, 0xb4, 0x54), // food
	rgb(0x3a, 0x46, 0x52), // dead snake
	rgb(0x4c, 0xc3, 0x8a), // snake 0  green
	rgb(0x36, 0xa3, 0xf7), // snake 1  blue
	rgb(0xc7, 0x92, 0xea), // snake 2  purple
	rgb(0x73, 0xd0, 0xff), // snake 3  cyan (kept distinct from the orange food)
	rgb(0xf0, 0x71, 0x78), // snake 4  red
	rgb(0xff, 0x9e, 0x64), // snake 5  orange
}

const (
	idxBG   = 0
	idxGrid = 1
	idxFood = 2
	idxDead = 3
	idxSnk0 = 4
)

func rgb(r, g, b uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: 0xff} }

func main() {
	out := flag.String("out", "docs/demo.gif", "output GIF path")
	frames := flag.Int("frames", 140, "number of ticks to render")
	cell := flag.Int("cell", 14, "pixels per cell")
	bots := flag.Int("bots", 4, "number of bots to keep in play")
	seed := flag.Int64("seed", 42, "simulation seed (deterministic output)")
	flag.Parse()

	cfg := game.Config{Width: 32, Height: 20, TickRate: 10, MaxFood: 6, Seed: *seed, StartLen: 3}
	m := newMatch(cfg, *bots)

	anim := &gif.GIF{LoopCount: 0} // 0 = loop forever
	for i := 0; i < *frames; i++ {
		m.step()
		anim.Image = append(anim.Image, render(m.world, *cell))
		anim.Delay = append(anim.Delay, 100/cfg.TickRate) // in 1/100s → 10fps
	}

	if dir := dirOf(*out); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := gif.EncodeAll(f, anim); err != nil {
		log.Fatal(err)
	}
	fi, _ := f.Stat()
	log.Printf("wrote %s (%d frames, %d bytes)", *out, len(anim.Image), fi.Size())
}

// --- demo match driver -----------------------------------------------------

type match struct {
	world  *game.World
	target int
	nextID game.PlayerID
	active map[game.PlayerID]bool
}

func newMatch(cfg game.Config, target int) *match {
	m := &match{world: game.NewWorld(cfg), target: target, active: map[game.PlayerID]bool{}}
	// Seed the first population and tick once so snakes exist before frame 0.
	m.world.Step(game.Input{Joins: m.fillJoins()})
	return m
}

// fillJoins tops the population up to target with new players.
func (m *match) fillJoins() []game.JoinEvent {
	var joins []game.JoinEvent
	for len(m.active)+len(joins) < m.target {
		id := m.nextID
		m.nextID++
		joins = append(joins, game.JoinEvent{ID: id, Name: "bot"})
		m.active[id] = true
	}
	return joins
}

// step advances one tick: AI picks directions, dead snakes are cleared and
// replaced so the arena stays lively.
func (m *match) step() {
	dirs := map[game.PlayerID]game.Dir{}
	var leaves []game.PlayerID
	for _, id := range sortedIDs(m.world) {
		s := m.world.Snakes[id]
		if !s.Alive {
			leaves = append(leaves, id)
			delete(m.active, id)
			continue
		}
		dirs[id] = chooseDir(m.world, id)
	}
	in := game.Input{Dirs: dirs, Leaves: leaves, Joins: m.fillJoins()}
	m.world.Step(in)
}

// --- greedy bot AI ---------------------------------------------------------

var dirDelta = map[game.Dir][2]int{
	game.DirUp: {0, -1}, game.DirDown: {0, 1}, game.DirLeft: {-1, 0}, game.DirRight: {1, 0},
}

// chooseDir heads toward the nearest food while avoiding the walls and any
// occupied cell on the next step. If nothing is safe it keeps its heading.
func chooseDir(w *game.World, id game.PlayerID) game.Dir {
	s := w.Snakes[id]
	head := s.Head()
	blocked := occupancy(w)

	target, hasFood := nearestFood(w, head)

	best := s.Dir
	bestScore := -1 << 30
	for _, d := range []game.Dir{game.DirUp, game.DirDown, game.DirLeft, game.DirRight} {
		if len(s.Body) > 1 && isReverse(d, s.Dir) {
			continue
		}
		dd := dirDelta[d]
		nx, ny := head.X+dd[0], head.Y+dd[1]
		if nx < 0 || ny < 0 || nx >= w.Cfg.Width || ny >= w.Cfg.Height {
			continue // wall
		}
		if blocked[[2]int{nx, ny}] {
			continue // body
		}
		score := 0
		if hasFood {
			// closer to food is better
			score = -(abs(nx-target.X) + abs(ny-target.Y))
		}
		if score > bestScore {
			bestScore = score
			best = d
		}
	}
	return best
}

func occupancy(w *game.World) map[[2]int]bool {
	set := map[[2]int]bool{}
	for _, id := range sortedIDs(w) {
		s := w.Snakes[id]
		if !s.Alive {
			continue
		}
		for _, p := range s.Body {
			set[[2]int{p.X, p.Y}] = true
		}
	}
	return set
}

func nearestFood(w *game.World, from game.Point) (game.Point, bool) {
	best := game.Point{}
	bestD := 1 << 30
	found := false
	for _, f := range w.Food {
		d := abs(f.X-from.X) + abs(f.Y-from.Y)
		if d < bestD {
			bestD, best, found = d, f, true
		}
	}
	return best, found
}

func isReverse(a, b game.Dir) bool {
	return (a == game.DirUp && b == game.DirDown) || (a == game.DirDown && b == game.DirUp) ||
		(a == game.DirLeft && b == game.DirRight) || (a == game.DirRight && b == game.DirLeft)
}

// --- rendering -------------------------------------------------------------

func render(w *game.World, cell int) *image.Paletted {
	width := w.Cfg.Width * cell
	height := w.Cfg.Height * cell
	img := image.NewPaletted(image.Rect(0, 0, width, height), palette)

	// background + faint grid
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x%cell == 0 || y%cell == 0 {
				img.SetColorIndex(x, y, idxGrid)
			} else {
				img.SetColorIndex(x, y, idxBG)
			}
		}
	}

	// food (small centred block)
	for _, f := range w.Food {
		fillCell(img, f.X, f.Y, cell, idxFood, cell/4)
	}

	// snakes
	for _, id := range sortedIDs(w) {
		s := w.Snakes[id]
		ci := uint8(idxSnk0 + int(id)%(len(palette)-idxSnk0))
		if !s.Alive {
			ci = idxDead
		}
		for i, p := range s.Body {
			pad := 2
			if i == 0 {
				pad = 1 // head slightly larger
			}
			fillCell(img, p.X, p.Y, cell, ci, pad)
		}
	}
	return img
}

// fillCell paints the cell at grid (cx,cy) leaving `pad` pixels of margin.
func fillCell(img *image.Paletted, cx, cy, cell int, ci uint8, pad int) {
	x0 := cx*cell + pad
	y0 := cy*cell + pad
	x1 := cx*cell + cell - pad
	y1 := cy*cell + cell - pad
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetColorIndex(x, y, ci)
		}
	}
}

// --- helpers ---------------------------------------------------------------

func sortedIDs(w *game.World) []game.PlayerID {
	ids := make([]game.PlayerID, 0, len(w.Snakes))
	for id := range w.Snakes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
