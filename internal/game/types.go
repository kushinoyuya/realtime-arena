// Package game holds the authoritative, deterministic simulation of the
// Snake arena. It has no knowledge of networking: the whole world advances
// through a single pure function, World.Step, which is what makes replays and
// (potentially) lockstep netcode possible. Given the same initial World and
// the same sequence of inputs, Step always produces the same result.
package game

// Dir is a movement direction. The zero value (DirNone) means "no change".
type Dir uint8

const (
	DirNone Dir = iota
	DirUp
	DirDown
	DirLeft
	DirRight
)

// String renders a Dir as the wire token used by the protocol.
func (d Dir) String() string {
	switch d {
	case DirUp:
		return "up"
	case DirDown:
		return "down"
	case DirLeft:
		return "left"
	case DirRight:
		return "right"
	default:
		return "none"
	}
}

// ParseDir maps a wire token to a Dir. Unknown tokens become DirNone so bad
// client input can never crash the simulation.
func ParseDir(s string) Dir {
	switch s {
	case "up":
		return DirUp
	case "down":
		return DirDown
	case "left":
		return DirLeft
	case "right":
		return DirRight
	default:
		return DirNone
	}
}

// delta returns the grid movement for a direction.
func (d Dir) delta() (int, int) {
	switch d {
	case DirUp:
		return 0, -1
	case DirDown:
		return 0, 1
	case DirLeft:
		return -1, 0
	case DirRight:
		return 1, 0
	default:
		return 0, 0
	}
}

// opposite reports whether a and b point in opposite directions. A snake may
// not reverse straight back into its own neck, so such input is rejected.
func opposite(a, b Dir) bool {
	return (a == DirUp && b == DirDown) ||
		(a == DirDown && b == DirUp) ||
		(a == DirLeft && b == DirRight) ||
		(a == DirRight && b == DirLeft)
}

// Point is a grid cell.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// PlayerID identifies a player within a single match.
type PlayerID int

// Snake is one player's body. Body[0] is the head.
type Snake struct {
	ID    PlayerID `json:"id"`
	Name  string   `json:"name"`
	Body  []Point  `json:"body"`
	Dir   Dir      `json:"-"`
	Alive bool     `json:"alive"`
	Score int      `json:"score"`
}

// Head returns the snake's head cell. Callers must ensure the body is
// non-empty (it always is for a snake created by the simulation).
func (s *Snake) Head() Point { return s.Body[0] }

// Config is the fixed ruleset of a match. It is part of the replay record so a
// recorded game can be re-simulated under identical rules.
type Config struct {
	Width    int   `json:"width"`
	Height   int   `json:"height"`
	TickRate int   `json:"tickRate"` // ticks per second, informational
	MaxFood  int   `json:"maxFood"`
	Seed     int64 `json:"seed"`
	StartLen int   `json:"startLen"`
}

// DefaultConfig returns a sensible arena configuration.
func DefaultConfig(seed int64) Config {
	return Config{
		Width:    40,
		Height:   30,
		TickRate: 10,
		MaxFood:  4,
		Seed:     seed,
		StartLen: 3,
	}
}
