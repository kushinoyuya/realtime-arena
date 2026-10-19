package replay

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/protocol"
)

// Frame is the board at one tick, in the same shape as a live snapshot so the
// browser can draw both with the same code.
type Frame struct {
	Tick   int                  `json:"tick"`
	Snakes []protocol.SnakeView `json:"snakes"`
	Food   []game.Point         `json:"food"`
}

// Playback is a match re-simulated from its event log.
type Playback struct {
	Meta     Meta    `json:"meta"`
	Verified bool    `json:"verified"` // re-simulation reached the recorded final state
	Frames   []Frame `json:"frames"`   // Frames[0] is the board before the first tick
}

// Play re-simulates rec tick by tick. Nothing but the seed and the inputs is
// used, so Verified is true exactly when those reproduce the recorded result.
func Play(rec *Record) Playback {
	w := game.NewWorld(rec.Replay.Cfg)
	frames := make([]Frame, 0, len(rec.Replay.Inputs)+1)
	frames = append(frames, frameOf(w))
	for _, in := range rec.Replay.Inputs {
		w.Step(in)
		frames = append(frames, frameOf(w))
	}
	return Playback{
		Meta:     rec.Meta,
		Verified: strconv.FormatUint(game.Fingerprint(w), 16) == rec.Meta.Fingerprint,
		Frames:   frames,
	}
}

func frameOf(w *game.World) Frame {
	ids := make([]game.PlayerID, 0, len(w.Snakes))
	for id := range w.Snakes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	f := Frame{Tick: w.Tick, Snakes: make([]protocol.SnakeView, 0, len(ids))}
	for _, id := range ids {
		s := w.Snakes[id]
		// copy: Step reuses and shifts body slices on later ticks
		body := append([]game.Point(nil), s.Body...)
		f.Snakes = append(f.Snakes, protocol.SnakeView{ID: s.ID, Name: s.Name, Body: body, Alive: s.Alive, Score: s.Score})
	}
	f.Food = append([]game.Point(nil), w.Food...)
	return f
}

// Handler serves the replay API:
//
//	GET /api/replays        list of stored matches (Meta), newest first
//	GET /api/replays/{id}   one match, re-simulated into frames (Playback)
func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/replays"), "/")
		if id == "" {
			writeJSON(w, s.List())
			return
		}
		if !validID.MatchString(id) {
			http.Error(w, "bad replay id", http.StatusBadRequest)
			return
		}
		rec, ok := s.Get(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, Play(rec))
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
