package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kushino/realtime-arena/internal/replay"
)

// quickHub returns a hub whose rooms forget offline players after 2 ticks and
// report idleness after idle ticks, so lifecycle tests finish in milliseconds.
func quickHub(idle int) *Hub {
	h := NewHub(HubConfig{TickRate: 100})
	h.tuneRoom = func(r *Room) {
		r.graceTicks = 2
		r.idleLimit = idle
	}
	return h
}

func (h *Hub) snapshot() (rooms, tokens int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms), len(h.tokens)
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestIdleRoomIsClosed: once its last player is gone, a room stops ticking
// and the hub forgets it and its tokens; the next join gets a fresh room.
func TestIdleRoomIsClosed(t *testing.T) {
	h := quickHub(3)
	c := fakeClient()
	room, id, token := h.Join(c, "p1")
	room.Leave(id, c)

	waitFor(t, "idle room to close", func() bool {
		rooms, tokens := h.snapshot()
		return rooms == 0 && tokens == 0
	})
	select {
	case <-room.quit:
	default:
		t.Fatal("room loop was not told to stop")
	}
	if _, _, err := h.Rejoin(fakeClient(), token); err == nil {
		t.Fatal("rejoin into a closed room should fail")
	}

	c2 := fakeClient()
	room2, _, _ := h.Join(c2, "p2")
	if room2 == room {
		t.Fatal("join reused a closed room")
	}
	if got := readSnapshots(c2, 100*time.Millisecond); len(got) == 0 {
		t.Fatal("new room is not ticking")
	}
}

// TestOccupiedRoomStaysOpen: idleness counts players, not elapsed time.
func TestOccupiedRoomStaysOpen(t *testing.T) {
	h := quickHub(3)
	h.Join(fakeClient(), "p1")
	time.Sleep(100 * time.Millisecond) // ~10 ticks, well past idleLimit
	if rooms, _ := h.snapshot(); rooms != 1 {
		t.Fatalf("rooms = %d, want 1", rooms)
	}
}

// TestReapedPlayerTokenIsForgotten: a slot that expires takes its reconnect
// token with it, while other players' tokens stay valid.
func TestReapedPlayerTokenIsForgotten(t *testing.T) {
	h := quickHub(1 << 30)
	c1 := fakeClient()
	room, id1, tok1 := h.Join(c1, "leaver")
	_, _, tok2 := h.Join(fakeClient(), "stayer")
	room.Leave(id1, c1)

	waitFor(t, "expired token to be dropped", func() bool {
		_, tokens := h.snapshot()
		return tokens == 1
	})
	h.mu.Lock()
	_, has1 := h.tokens[tok1]
	_, has2 := h.tokens[tok2]
	h.mu.Unlock()
	if has1 || !has2 {
		t.Fatalf("tokens: leaver kept=%v, stayer kept=%v", has1, has2)
	}
}

// TestJoinAfterDeadBoardStartsFreshRound is a regression test: if every snake
// died while nobody was online, the next joiner used to continue that dead
// world, so the following recording replayed both rounds.
func TestJoinAfterDeadBoardStartsFreshRound(t *testing.T) {
	cfg := fastCfg()
	cfg.Width, cfg.Height = 10, 8
	r := buildRoom(0, cfg)
	got := make(chan replay.Record, 4)
	r.onRoundEnd = func(rec replay.Record) { got <- rec }
	go r.loop()
	defer close(r.quit)

	first := fakeClient()
	id := r.addPlayer(first, "first")
	r.Leave(id, first) // offline: the snake stays and soon hits a wall
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("first round never ended")
	}

	r.addPlayer(fakeClient(), "second")
	select {
	case rec := <-got:
		if len(rec.Meta.Players) != 1 || rec.Meta.Players[0].Name != "second" {
			t.Fatalf("second recording carries the old round: players=%+v", rec.Meta.Players)
		}
		if !replay.Play(&rec).Verified {
			t.Error("second recording does not re-simulate")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second round never ended")
	}
}

func TestOriginAllowed(t *testing.T) {
	h := NewHub(HubConfig{AllowedOrigins: []string{"https://Arena.example/"}})
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},                        // non-browser client
		{"http://game.local:8080", true},  // same origin as Host
		{"https://arena.example", true},   // configured
		{"https://evil.example", false},   // cross-site page
		{"null", false},                   // sandboxed iframe / file://
		{"http://game.local:9999", false}, // same host, other port
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://game.local:8080/ws", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := h.originAllowed(r); got != c.want {
			t.Errorf("origin %q: allowed=%v, want %v", c.origin, got, c.want)
		}
	}
}
