package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/protocol"
)

// fakeClient is a Client with no socket: enqueued bytes land in its channel
// where the test can read them. This lets us exercise the full room goroutine
// (tick loop, broadcast, matchmaking, reconnection) without opening a port,
// which the sandbox forbids anyway.
func fakeClient() *Client {
	return &Client{send: make(chan []byte, 512), done: make(chan struct{})}
}

// TestEnqueueAfterDisconnectDoesNotPanic is a regression test for a
// "send on closed channel" panic: the room loop could call enqueue on a client
// that had just disconnected. enqueue must stay panic-free and non-blocking even
// after the client is done and its buffer is full.
func TestEnqueueAfterDisconnectDoesNotPanic(t *testing.T) {
	c := fakeClient()
	close(c.done) // simulate the client tearing down

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			c.enqueue([]byte("x")) // must never panic or block
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked after client disconnect")
	}
}

func fastCfg() game.Config {
	c := game.DefaultConfig(1)
	c.TickRate = 100 // 10ms ticks so the test runs quickly
	return c
}

func readSnapshots(c *Client, d time.Duration) []protocol.Snapshot {
	var out []protocol.Snapshot
	deadline := time.After(d)
	for {
		select {
		case b := <-c.send:
			var s protocol.Snapshot
			if json.Unmarshal(b, &s) == nil && s.Type == protocol.TypeSnapshot {
				out = append(out, s)
			}
		case <-deadline:
			return out
		}
	}
}

func TestRoomTicksAndBroadcasts(t *testing.T) {
	r := newRoom(0, fastCfg())
	c := fakeClient()
	id := r.addPlayer(c, "p1")
	if id != 0 {
		t.Fatalf("first player id = %d, want 0", id)
	}

	snaps := readSnapshots(c, 250*time.Millisecond)
	if len(snaps) < 3 {
		t.Fatalf("expected several snapshots, got %d", len(snaps))
	}
	// Ticks increase by one within a round; a drop back to 0 marks a new round
	// (which starts automatically once every snake has died).
	for i := 1; i < len(snaps); i++ {
		prev, cur := snaps[i-1].Tick, snaps[i].Tick
		if cur != prev+1 && cur != 0 {
			t.Fatalf("unexpected tick sequence: %d then %d", prev, cur)
		}
	}
	// Our snake should appear in the state.
	found := false
	for _, sv := range snaps[len(snaps)-1].Snakes {
		if sv.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("our snake never appeared in a snapshot")
	}
}

func TestInputIsApplied(t *testing.T) {
	r := newRoom(0, fastCfg())
	c := fakeClient()
	id := r.addPlayer(c, "p1")

	// Let the snake exist, then steer it down. We look for any pair of
	// consecutive-tick snapshots (same round) where the head moved down by one
	// — that isolates one simulation step and is immune to round restarts.
	_ = readSnapshots(c, 60*time.Millisecond)
	r.Input(id, game.DirDown)
	snaps := readSnapshots(c, 200*time.Millisecond)

	type sample struct {
		tick  int
		y     int
		alive bool
	}
	var samples []sample
	for _, s := range snaps {
		for _, sv := range s.Snakes {
			if sv.ID == id && len(sv.Body) > 0 {
				samples = append(samples, sample{tick: s.Tick, y: sv.Body[0].Y, alive: sv.Alive})
			}
		}
	}
	movedDown := false
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		if b.tick == a.tick+1 && a.alive && b.alive && b.y == a.y+1 {
			movedDown = true
			break
		}
	}
	if !movedDown {
		t.Fatalf("head never moved down by one after DirDown; samples=%v", samples)
	}
}

func TestReconnectWithinGrace(t *testing.T) {
	r := newRoom(0, fastCfg())
	c := fakeClient()
	id := r.addPlayer(c, "p1")
	_ = readSnapshots(c, 40*time.Millisecond)

	// Simulate a dropped connection.
	r.Leave(id, c)
	time.Sleep(30 * time.Millisecond)

	// Within the grace period the slot must still be reclaimable.
	c2 := fakeClient()
	if !r.reattach(c2, id) {
		t.Fatal("reattach within grace period failed")
	}
	// The reattached client should now receive snapshots.
	if got := readSnapshots(c2, 120*time.Millisecond); len(got) == 0 {
		t.Fatal("no snapshots after reattach")
	}
}

func TestMatchmakingFillsAndSpillsRooms(t *testing.T) {
	hub := NewHub(HubConfig{Capacity: 2, TickRate: 100})
	c1, c2, c3 := fakeClient(), fakeClient(), fakeClient()
	r1, _, _ := hub.Join(c1, "a")
	r2, _, _ := hub.Join(c2, "b")
	r3, _, _ := hub.Join(c3, "c")

	if r1 != r2 {
		t.Fatal("first two players should share a room")
	}
	if r3 == r1 {
		t.Fatal("third player should spill into a new room (capacity 2)")
	}
}

func TestRejoinUnknownTokenFails(t *testing.T) {
	hub := NewHub(HubConfig{TickRate: 100})
	if _, _, err := hub.Rejoin(fakeClient(), "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown token")
	}
}

// TestStaleLeaveAfterRejoinKeepsNewClient is a regression test: a client whose
// network dropped can rejoin on a new connection before the server notices the
// old one is dead. When the old connection's disconnect finally arrives, it
// must not mark the slot offline — the new connection owns it now.
func TestStaleLeaveAfterRejoinKeepsNewClient(t *testing.T) {
	r := newRoom(0, fastCfg())
	oldConn := fakeClient()
	id := r.addPlayer(oldConn, "p1")
	_ = readSnapshots(oldConn, 40*time.Millisecond)

	newConn := fakeClient()
	if !r.reattach(newConn, id) {
		t.Fatal("reattach failed")
	}
	r.Leave(id, oldConn) // late disconnect of the superseded connection
	time.Sleep(30 * time.Millisecond)

	_ = readSnapshots(newConn, 20*time.Millisecond) // drain pre-Leave snapshots
	if got := readSnapshots(newConn, 100*time.Millisecond); len(got) == 0 {
		t.Fatal("stale Leave knocked the rejoined connection offline")
	}
}
