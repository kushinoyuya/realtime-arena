package server

import (
	"sort"
	"sync/atomic"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/protocol"
)

// A Room is one match. All game state lives here and is owned by a single
// goroutine (loop). Every mutation arrives on a channel, so there are no locks
// around the world and no data races — the concurrency model is "share memory
// by communicating". Client goroutines only ever send on these channels.
type Room struct {
	id  int
	cfg game.Config

	// Matchmaking visibility for the hub (atomic; everything else is loop-owned).
	count    int32
	seedStep int64

	// Inbound channels into the loop goroutine.
	joinCh   chan joinReq
	rejoinCh chan rejoinReq
	inputCh  chan inputReq
	leaveCh  chan game.PlayerID
	quit     chan struct{}

	// --- below here: touched only by loop() ---
	world         *game.World
	replay        *game.Replay
	clients       map[game.PlayerID]*Client // nil value => offline (awaiting rejoin)
	names         map[game.PlayerID]string
	offlineSince  map[game.PlayerID]int // tick the player went offline; -1 if online
	nextID        game.PlayerID
	pendingJoins  []game.JoinEvent
	pendingLeaves []game.PlayerID
	pendingDirs   map[game.PlayerID]game.Dir
	graceTicks    int
}

type joinReq struct {
	client *Client
	name   string
	resp   chan game.PlayerID
}

type rejoinReq struct {
	client *Client
	id     game.PlayerID
	resp   chan bool
}

type inputReq struct {
	id  game.PlayerID
	dir game.Dir
}

func newRoom(id int, cfg game.Config) *Room {
	r := &Room{
		id:           id,
		cfg:          cfg,
		joinCh:       make(chan joinReq),
		rejoinCh:     make(chan rejoinReq),
		inputCh:      make(chan inputReq, 256),
		leaveCh:      make(chan game.PlayerID, 64),
		quit:         make(chan struct{}),
		world:        game.NewWorld(cfg),
		replay:       game.NewReplay(cfg),
		clients:      make(map[game.PlayerID]*Client),
		names:        make(map[game.PlayerID]string),
		offlineSince: make(map[game.PlayerID]int),
		pendingDirs:  make(map[game.PlayerID]game.Dir),
		graceTicks:   cfg.TickRate * 4,
	}
	go r.loop()
	return r
}

// loop is the single owner of all room state.
func (r *Room) loop() {
	interval := time.Second / time.Duration(r.cfg.TickRate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.quit:
			return
		case req := <-r.joinCh:
			id := r.nextID
			r.nextID++
			r.clients[id] = req.client
			r.names[id] = req.name
			r.offlineSince[id] = -1
			r.pendingJoins = append(r.pendingJoins, game.JoinEvent{ID: id, Name: req.name})
			req.resp <- id
		case req := <-r.rejoinCh:
			if _, ok := r.world.Snakes[req.id]; ok {
				r.clients[req.id] = req.client
				r.offlineSince[req.id] = -1
				req.resp <- true
			} else {
				req.resp <- false
			}
		case in := <-r.inputCh:
			r.pendingDirs[in.id] = in.dir
		case id := <-r.leaveCh:
			// Disconnect: keep the snake for a grace period so a reconnecting
			// client can reclaim it; just mark the slot offline for now.
			if s, ok := r.offlineSince[id]; ok && s == -1 {
				r.offlineSince[id] = r.world.Tick
			}
			if _, ok := r.clients[id]; ok {
				r.clients[id] = nil
			}
		case <-ticker.C:
			r.tick()
		}
	}
}

// tick advances the authoritative simulation by one step and broadcasts the
// result. This is the only place the world changes.
func (r *Room) tick() {
	// Reap players whose grace period has expired.
	var reaped []game.PlayerID
	for id, since := range r.offlineSince {
		if since >= 0 && r.world.Tick-since >= r.graceTicks {
			reaped = append(reaped, id)
		}
	}
	// Deterministic leave order so the recorded replay reproduces exactly.
	leaves := append([]game.PlayerID(nil), r.pendingLeaves...)
	leaves = append(leaves, reaped...)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i] < leaves[j] })

	in := game.Input{
		Joins:  r.pendingJoins,
		Leaves: leaves,
		Dirs:   r.pendingDirs,
	}
	r.world.Step(in)
	r.replay.Record(in)

	for _, id := range reaped {
		delete(r.clients, id)
		delete(r.names, id)
		delete(r.offlineSince, id)
		atomic.AddInt32(&r.count, -1)
	}

	r.pendingJoins = nil
	r.pendingLeaves = nil
	r.pendingDirs = make(map[game.PlayerID]game.Dir)

	// If everyone is dead but players are still connected, start a fresh round.
	if r.world.AliveCount() == 0 && r.onlineCount() > 0 {
		r.restart()
	}

	r.broadcast()
}

// restart begins a new round with a new seed, re-joining everyone still online.
func (r *Room) restart() {
	r.seedStep++
	r.cfg.Seed = r.cfg.Seed*6364136223846793005 + 1442695040888963407 + r.seedStep
	r.world = game.NewWorld(r.cfg)
	r.replay = game.NewReplay(r.cfg)

	ids := make([]game.PlayerID, 0, len(r.clients))
	for id, c := range r.clients {
		if c != nil {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	r.pendingJoins = nil
	for _, id := range ids {
		r.pendingJoins = append(r.pendingJoins, game.JoinEvent{ID: id, Name: r.names[id]})
		r.offlineSince[id] = -1
	}
}

// broadcast marshals the current state once and fans the same bytes out to all
// online clients. Marshalling once (not once per client) is what keeps the
// per-tick cost flat as the player count grows. Clients learn their own id from
// the Welcome message, so Snapshot.You is left unset here.
func (r *Room) broadcast() {
	views := make([]protocol.SnakeView, 0, len(r.world.Snakes))
	for _, id := range sortedSnakeIDs(r.world) {
		s := r.world.Snakes[id]
		views = append(views, protocol.SnakeView{
			ID: s.ID, Name: s.Name, Body: s.Body, Alive: s.Alive, Score: s.Score,
		})
	}
	snap := protocol.Snapshot{
		Type:         protocol.TypeSnapshot,
		Tick:         r.world.Tick,
		ServerTimeMs: time.Now().UnixNano() / int64(time.Millisecond),
		Snakes:       views,
		Food:         r.world.Food,
	}
	b := protocol.MustEncode(snap)
	for _, id := range sortedClientIDs(r.clients) {
		if c := r.clients[id]; c != nil {
			c.enqueue(b)
		}
	}
}

func (r *Room) onlineCount() int {
	n := 0
	for _, c := range r.clients {
		if c != nil {
			n++
		}
	}
	return n
}

// --- methods callable from other goroutines (channel sends only) ---

func (r *Room) addPlayer(c *Client, name string) game.PlayerID {
	resp := make(chan game.PlayerID, 1)
	select {
	case r.joinCh <- joinReq{client: c, name: name, resp: resp}:
		return <-resp
	case <-r.quit:
		return -1
	}
}

func (r *Room) reattach(c *Client, id game.PlayerID) bool {
	resp := make(chan bool, 1)
	select {
	case r.rejoinCh <- rejoinReq{client: c, id: id, resp: resp}:
		return <-resp
	case <-r.quit:
		return false
	}
}

// Input records a direction request. Safe to call from any goroutine.
func (r *Room) Input(id game.PlayerID, dir game.Dir) {
	if dir == game.DirNone {
		return
	}
	select {
	case r.inputCh <- inputReq{id: id, dir: dir}:
	case <-r.quit:
	default:
		// input buffer full under extreme load: drop (next input supersedes it)
	}
}

// Leave marks a disconnect. Safe to call from any goroutine.
func (r *Room) Leave(id game.PlayerID) {
	select {
	case r.leaveCh <- id:
	case <-r.quit:
	}
}

func sortedSnakeIDs(w *game.World) []game.PlayerID {
	ids := make([]game.PlayerID, 0, len(w.Snakes))
	for id := range w.Snakes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sortedClientIDs(m map[game.PlayerID]*Client) []game.PlayerID {
	ids := make([]game.PlayerID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
