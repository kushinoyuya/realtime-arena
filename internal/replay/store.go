// Package replay keeps finished matches and plays them back.
//
// A match is stored as its event log only — the ruleset (with the RNG seed)
// plus the per-tick inputs — because game.World.Step is deterministic: the
// full match, every frame of it, is re-derived on demand by re-simulating.
// That keeps each recording to a few KB, and doubles as an integrity check:
// the fingerprint of the final state is saved alongside, so a recording whose
// inputs were altered no longer re-simulates to the same result.
package replay

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
)

// PlayerResult is one snake's outcome, for listing a match without replaying it.
type PlayerResult struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Meta describes a stored match.
type Meta struct {
	ID          string         `json:"id"`
	Room        int            `json:"room"`
	Seed        int64          `json:"seed"`
	Ticks       int            `json:"ticks"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	TickRate    int            `json:"tickRate"`
	Players     []PlayerResult `json:"players"` // highest score first
	EndedAt     time.Time      `json:"endedAt"`
	Fingerprint string         `json:"fingerprint"` // of the final World, hex
	StoredBytes int            `json:"storedBytes"` // size of the saved record
}

// Record is what is persisted: the description plus the event log.
type Record struct {
	Meta   Meta         `json:"meta"`
	Replay *game.Replay `json:"replay"`
}

// NewRecord captures a finished match. final must be the World produced by
// rep (i.e. rep.Simulate() would equal it); its fingerprint is what later
// playback is verified against.
//
// The returned record shares nothing mutable with the caller: the input slice
// is capped at its current length, so the room may keep appending to its own
// replay without touching this copy.
func NewRecord(room int, rep *game.Replay, final *game.World, endedAt time.Time) Record {
	inputs := rep.Inputs[:len(rep.Inputs):len(rep.Inputs)]
	var players []PlayerResult
	for _, s := range final.Snakes {
		players = append(players, PlayerResult{Name: s.Name, Score: s.Score})
	}
	sort.Slice(players, func(i, j int) bool {
		if players[i].Score != players[j].Score {
			return players[i].Score > players[j].Score
		}
		return players[i].Name < players[j].Name
	})
	return Record{
		Meta: Meta{
			Room:        room,
			Seed:        rep.Cfg.Seed,
			Ticks:       len(inputs),
			Width:       rep.Cfg.Width,
			Height:      rep.Cfg.Height,
			TickRate:    rep.Cfg.TickRate,
			Players:     players,
			EndedAt:     endedAt.UTC(),
			Fingerprint: strconv.FormatUint(game.Fingerprint(final), 16),
		},
		Replay: &game.Replay{Cfg: rep.Cfg, Inputs: inputs},
	}
}

// validID guards file names: IDs come back in from URLs.
var validID = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// Store holds the most recent matches in memory and, when dir is set, on disk
// as one JSON file each. It is bounded: past max, the oldest match is dropped
// from both. Safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	dir  string
	max  int
	seq  int
	recs []*Record // newest first
}

// NewStore opens a store, loading any recordings already in dir. An empty dir
// keeps recordings in memory only.
func NewStore(dir string, max int) (*Store, error) {
	if max <= 0 {
		max = 50
	}
	s := &Store{dir: dir, max: max}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		b, err := ioutil.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var rec Record
		if err := json.Unmarshal(b, &rec); err != nil || rec.Replay == nil || !validID.MatchString(rec.Meta.ID) {
			continue // not ours, or damaged: skip rather than refuse to start
		}
		s.recs = append(s.recs, &rec)
	}
	s.sortAndTrim()
	return s, nil
}

// Add stores a finished match, assigning its ID, and returns that ID.
func (s *Store) Add(rec Record) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	rec.Meta.ID = fmt.Sprintf("%s-r%d-%d", rec.Meta.EndedAt.UTC().Format("20060102-150405"), rec.Meta.Room, s.seq)
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	rec.Meta.StoredBytes = len(b)

	if s.dir != "" {
		// re-marshal so the file carries StoredBytes too; write-then-rename
		// so a crash never leaves a half-written recording behind
		if b, err = json.Marshal(rec); err != nil {
			return "", err
		}
		path := s.path(rec.Meta.ID)
		if err := ioutil.WriteFile(path+".tmp", b, 0644); err != nil {
			return "", err
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			return "", err
		}
	}
	s.recs = append([]*Record{&rec}, s.recs...)
	s.sortAndTrim()
	return rec.Meta.ID, nil
}

// List returns the stored matches, newest first.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, len(s.recs))
	for i, r := range s.recs {
		out[i] = r.Meta
	}
	return out
}

// Get returns one stored match.
func (s *Store) Get(id string) (*Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.Meta.ID == id {
			return r, true
		}
	}
	return nil, false
}

// sortAndTrim orders newest first and evicts past max. Caller holds mu.
func (s *Store) sortAndTrim() {
	sort.SliceStable(s.recs, func(i, j int) bool {
		a, b := s.recs[i].Meta, s.recs[j].Meta
		if !a.EndedAt.Equal(b.EndedAt) {
			return a.EndedAt.After(b.EndedAt)
		}
		return a.ID > b.ID
	})
	for len(s.recs) > s.max {
		old := s.recs[len(s.recs)-1]
		s.recs = s.recs[:len(s.recs)-1]
		if s.dir != "" {
			_ = os.Remove(s.path(old.Meta.ID)) // best effort; a leftover file is reloaded and trimmed next start
		}
	}
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, strings.TrimSpace(id)+".json")
}
