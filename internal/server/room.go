package server

import (
	"sort"
	"sync/atomic"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/protocol"
	"github.com/kushino/realtime-arena/internal/replay"
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

	// Instrumentation (set by the hub; may be nil in isolated unit tests).
	metrics *appMetrics

	// chaosTickMs injects artificial per-tick latency for incident drills
	// (see docs/RUNBOOK.md). 0 = disabled.
	chaosTickMs int

	// onRoundEnd receives each finished round (every snake dead). It is called
	// from the loop goroutine, so it must not block; the hub hands the record
	// off to the replay store asynchronously. nil = recording disabled.
	onRoundEnd func(replay.Record)

	// Inbound channels into the loop goroutine.
	joinCh   chan joinReq
	rejoinCh chan rejoinReq
	inputCh  chan inputReq
	leaveCh  chan leaveReq
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
	roundSaved    bool // the current all-dead state has already been recorded
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

// leaveReq names the connection that went away, not just the slot: after a
// rejoin the slot belongs to a newer connection, and the old one's late
// disconnect must not knock it offline.
type leaveReq struct {
	id     game.PlayerID
	client *Client
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
		leaveCh:      make(chan leaveReq, 64),
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
		case req := <-r.leaveCh:
			// Disconnect: keep the snake for a grace period so a reconnecting
			// client can reclaim it; just mark the slot offline for now.
			// Ignore a stale connection whose slot was already reclaimed.
			if cur, ok := r.clients[req.id]; !ok || cur != req.client {
				break
			}
			r.clients[req.id] = nil
			if s, ok := r.offlineSince[req.id]; ok && s == -1 {
				r.offlineSince[req.id] = r.world.Tick
			}
		case <-ticker.C:
			r.tick()
		}
	}
}

// tick advances the authoritative simulation by one step and broadcasts the
// result. This is the only place the world changes.
func (r *Room) tick() {
	start := time.Now()
	defer func() { r.metrics.observeTick(time.Since(start).Seconds()) }()

	if r.chaosTickMs > 0 {
		// Injected fault: makes ticks breach the latency budget so an operator
		// can watch arena_slow_ticks_total rise and the SLO burn. Drill only.
		time.Sleep(time.Duration(r.chaosTickMs) * time.Millisecond)
	}

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

	r.recordRoundEnd()

	// If everyone is dead but players are still connected, start a fresh round.
	if r.world.AliveCount() == 0 && r.onlineCount() > 0 {
		r.restart()
	}

	r.broadcast()
}

// recordRoundEnd hands the replay to onRoundEnd the first tick on which every
// snake is dead. NewRecord copies what it needs, so the room keeps owning (and
// may keep appending to) r.replay.
func (r *Room) recordRoundEnd() {
	if r.world.AliveCount() > 0 {
		r.roundSaved = false // someone joined a dead board: a new ending is coming
		return
	}
	if r.roundSaved || len(r.world.Snakes) == 0 || r.onRoundEnd == nil {
		return
	}
	r.roundSaved = true
	r.onRoundEnd(replay.NewRecord(r.id, r.replay, r.world, time.Now()))
}

// restart begins a new round with a new seed, re-joining everyone still online.
func (r *Room) restart() {
	r.seedStep++
	r.cfg.Seed = r.cfg.Seed*6364136223846793005 + 1442695040888963407 + r.seedStep
	r.world = game.NewWorld(r.cfg)
	r.replay = game.NewReplay(r.cfg)
	r.roundSaved = false

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
	sent := 0
	for _, id := range sortedClientIDs(r.clients) {
		if c := r.clients[id]; c != nil {
			c.enqueue(b)
			sent++
		}
	}
	if r.metrics != nil && sent > 0 {
		r.metrics.snapshots.Add(uint64(sent))
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
		if r.metrics != nil {
			r.metrics.inputs.Inc()
		}
	case <-r.quit:
	default:
		// input buffer full under extreme load: drop (next input supersedes it)
	}
}

// Leave marks a disconnect of connection c from slot id. Safe to call from any
// goroutine.
func (r *Room) Leave(id game.PlayerID, c *Client) {
	select {
	case r.leaveCh <- leaveReq{id: id, client: c}:
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
