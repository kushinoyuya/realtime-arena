package replay

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
)

// playMatch runs a small deterministic match until every snake is dead (or a
// tick limit) and returns the recording and the final world.
func playMatch(t *testing.T, seed int64) (*game.Replay, *game.World) {
	t.Helper()
	cfg := game.DefaultConfig(seed)
	cfg.Width, cfg.Height = 12, 10
	w := game.NewWorld(cfg)
	rep := game.NewReplay(cfg)

	step := func(in game.Input) {
		w.Step(in)
		rep.Record(in)
	}
	step(game.Input{Joins: []game.JoinEvent{{ID: 0, Name: "alice"}, {ID: 1, Name: "bob"}}})
	// some turning first so the log has real inputs, then no input at all:
	// a snake going straight is guaranteed to hit a wall within a board width
	dirs := []game.Dir{game.DirUp, game.DirLeft, game.DirDown, game.DirRight}
	for i := 0; i < 20 && w.AliveCount() > 0; i++ {
		step(game.Input{Dirs: map[game.PlayerID]game.Dir{0: dirs[i/3%4], 1: dirs[i/5%4]}})
	}
	for i := 0; i < cfg.Width+cfg.Height && w.AliveCount() > 0; i++ {
		step(game.Input{})
	}
	if w.AliveCount() != 0 {
		t.Fatalf("match did not end")
	}
	return rep, w
}

var ended = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestPlayReproducesAndVerifies(t *testing.T) {
	rep, final := playMatch(t, 7)
	rec := NewRecord(3, rep, final, ended)

	pb := Play(&rec)
	if !pb.Verified {
		t.Fatal("re-simulation should match the recorded final state")
	}
	if len(pb.Frames) != rep.Len()+1 {
		t.Fatalf("frames = %d, want ticks+1 = %d", len(pb.Frames), rep.Len()+1)
	}
	last := pb.Frames[len(pb.Frames)-1]
	if last.Tick != final.Tick || len(last.Snakes) != 2 || last.Snakes[0].Alive || last.Snakes[1].Alive {
		t.Errorf("last frame = %+v", last)
	}
	if rec.Meta.Players[0].Score < rec.Meta.Players[1].Score {
		t.Errorf("players should be sorted by score: %+v", rec.Meta.Players)
	}
}

func TestPlayDetectsTamperedInputs(t *testing.T) {
	rep, final := playMatch(t, 7)
	rec := NewRecord(0, rep, final, ended)

	// a "cheat": rewrite one of alice's inputs in the stored log
	for i := range rec.Replay.Inputs {
		if d, ok := rec.Replay.Inputs[i].Dirs[0]; ok && d != game.DirRight {
			tampered := make([]game.Input, len(rec.Replay.Inputs))
			copy(tampered, rec.Replay.Inputs)
			tampered[i] = game.Input{Dirs: map[game.PlayerID]game.Dir{0: game.DirRight, 1: rec.Replay.Inputs[i].Dirs[1]}}
			rec.Replay = &game.Replay{Cfg: rec.Replay.Cfg, Inputs: tampered}
			break
		}
	}
	if Play(&rec).Verified {
		t.Error("altered inputs must not verify against the recorded fingerprint")
	}
}

func TestNewRecordIsIsolatedFromLaterAppends(t *testing.T) {
	rep, final := playMatch(t, 7)
	rec := NewRecord(0, rep, final, ended)
	n := len(rec.Replay.Inputs)

	rep.Record(game.Input{}) // the room keeps ticking after the round ends
	if len(rec.Replay.Inputs) != n || !Play(&rec).Verified {
		t.Error("appending to the room's replay must not change a captured record")
	}
}

func TestStorePersistsReloadsAndTrims(t *testing.T) {
	dir, err := ioutil.TempDir("", "replays")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	s, err := NewStore(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		rep, final := playMatch(t, int64(10+i))
		id, err := s.Add(NewRecord(i, rep, final, ended.Add(time.Duration(i)*time.Minute)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	list := s.List()
	if len(list) != 2 || list[0].ID != ids[2] || list[1].ID != ids[1] {
		t.Fatalf("list = %+v, want the two newest", list)
	}
	if list[0].StoredBytes == 0 {
		t.Error("StoredBytes not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Errorf("files on disk = %d, want 2 (oldest evicted)", len(files))
	}

	// a restart reloads from disk, and the JSON round trip still verifies
	s2, err := NewStore(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := s2.Get(ids[2])
	if !ok || !Play(rec).Verified {
		t.Errorf("reloaded record missing or not verified (ok=%v)", ok)
	}
}

func TestHandler(t *testing.T) {
	s, _ := NewStore("", 10)
	rep, final := playMatch(t, 7)
	id, _ := s.Add(NewRecord(0, rep, final, ended))
	h := s.Handler()

	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}

	rr := get("/api/replays")
	var list []Meta
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].ID != id {
		t.Errorf("list: %d %s", rr.Code, rr.Body)
	}

	rr = get("/api/replays/" + id)
	var pb Playback
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &pb) != nil || !pb.Verified || len(pb.Frames) == 0 {
		t.Errorf("get: %d verified=%v frames=%d", rr.Code, pb.Verified, len(pb.Frames))
	}

	if rr = get("/api/replays/nope"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rr.Code)
	}
	if rr = get("/api/replays/..%2f..%2fetc"); rr.Code != http.StatusBadRequest {
		t.Errorf("traversal-looking id: %d, want 400", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/replays", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rr.Code)
	}
}
