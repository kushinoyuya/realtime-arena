package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/replay"
	"github.com/kushino/realtime-arena/internal/ws"
)

// Hub performs matchmaking: it places each joining connection into an open room
// (creating one when all rooms are full) and keeps the reconnect-token registry.
// Rooms themselves are self-contained goroutines; the hub only routes.
type Hub struct {
	mu          sync.Mutex
	rooms       []*Room
	tokens      map[string]tokenInfo
	cap         int32
	nextRoomID  int
	seedSeq     int64
	width       int
	height      int
	tickRate    int
	chaosTickMs int
	replays     *replay.Store
	origins     map[string]bool
	metrics     *appMetrics

	// tuneRoom, if set, adjusts each new room before its loop starts. Tests
	// use it to shorten the grace and idle periods.
	tuneRoom func(*Room)
}

type tokenInfo struct {
	room *Room
	id   game.PlayerID
}

// HubConfig configures matchmaking and the room ruleset.
type HubConfig struct {
	Capacity    int // players per room
	Width       int
	Height      int
	TickRate    int
	ChaosTickMs int           // injected per-tick latency for incident drills (0 = off)
	Replays     *replay.Store // where finished rounds are recorded (nil = off)

	// AllowedOrigins lists extra browser origins (e.g. "https://example.com")
	// that may open /ws. Same-origin pages are always allowed.
	AllowedOrigins []string
}

// NewHub creates an empty hub. Zero-valued config fields fall back to defaults.
func NewHub(cfg HubConfig) *Hub {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 8
	}
	d := game.DefaultConfig(0)
	if cfg.Width <= 0 {
		cfg.Width = d.Width
	}
	if cfg.Height <= 0 {
		cfg.Height = d.Height
	}
	if cfg.TickRate <= 0 {
		cfg.TickRate = d.TickRate
	}
	origins := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		origins[strings.ToLower(strings.TrimRight(o, "/"))] = true
	}
	return &Hub{
		tokens:      make(map[string]tokenInfo),
		origins:     origins,
		cap:         int32(cfg.Capacity),
		width:       cfg.Width,
		height:      cfg.Height,
		tickRate:    cfg.TickRate,
		chaosTickMs: cfg.ChaosTickMs,
		replays:     cfg.Replays,
		metrics:     newAppMetrics(),
	}
}

// Join reserves a slot in an open room and registers the player. It returns the
// room, the assigned player id, and a reconnect token.
func (h *Hub) Join(c *Client, name string) (*Room, game.PlayerID, string) {
	h.mu.Lock()
	var room *Room
	for _, rm := range h.rooms {
		if atomic.LoadInt32(&rm.count) < h.cap {
			room = rm
			break
		}
	}
	if room == nil {
		cfg := game.DefaultConfig(time.Now().UnixNano() + h.seedSeq)
		cfg.Width, cfg.Height, cfg.TickRate = h.width, h.height, h.tickRate
		h.seedSeq++
		room = h.newRoomLocked(cfg)
	}
	atomic.AddInt32(&room.count, 1) // reserve the slot before we release the lock
	token := h.newToken()
	h.mu.Unlock()

	h.metrics.joins.Inc()
	id := room.addPlayer(c, name)

	h.mu.Lock()
	h.tokens[token] = tokenInfo{room: room, id: id}
	h.mu.Unlock()
	return room, id, token
}

// Rejoin reattaches a reconnecting client to its previous slot, if the snake is
// still on the board (within the room's grace period).
func (h *Hub) Rejoin(c *Client, token string) (*Room, game.PlayerID, error) {
	h.mu.Lock()
	info, ok := h.tokens[token]
	h.mu.Unlock()
	if !ok {
		return nil, 0, errors.New("unknown token")
	}
	if !info.room.reattach(c, info.id) {
		return nil, 0, errors.New("slot expired")
	}
	return info.room, info.id, nil
}

// newRoomLocked creates, registers and starts a room. h.mu must be held.
func (h *Hub) newRoomLocked(cfg game.Config) *Room {
	room := buildRoom(h.nextRoomID, cfg)
	room.metrics = h.metrics
	room.chaosTickMs = h.chaosTickMs
	if h.replays != nil {
		room.onRoundEnd = h.saveReplay
	}
	room.onReap = func(ids []game.PlayerID) { h.forgetPlayers(room, ids) }
	room.onIdle = func() { h.closeIfIdle(room) }
	if h.tuneRoom != nil {
		h.tuneRoom(room)
	}
	h.nextRoomID++
	h.rooms = append(h.rooms, room)
	go room.loop()
	return room
}

// forgetPlayers drops the reconnect tokens of players whose slot has expired;
// without this the token map would grow for the life of the process.
func (h *Hub) forgetPlayers(room *Room, ids []game.PlayerID) {
	gone := make(map[game.PlayerID]bool, len(ids))
	for _, id := range ids {
		gone[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for tok, info := range h.tokens {
		if info.room == room && gone[info.id] {
			delete(h.tokens, tok)
		}
	}
}

// closeIfIdle stops an empty room's goroutine and ticker and forgets it.
// Join reserves a slot (count++) while holding h.mu, so checking count under
// the same lock means a room can never be closed with a joiner on its way in.
// It is called from the room's own loop; that is safe because nothing holding
// h.mu ever waits on a room.
func (h *Hub) closeIfIdle(room *Room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if atomic.LoadInt32(&room.count) > 0 {
		return
	}
	for i, rm := range h.rooms {
		if rm == room {
			h.rooms = append(h.rooms[:i], h.rooms[i+1:]...)
			break
		}
	}
	for tok, info := range h.tokens {
		if info.room == room {
			delete(h.tokens, tok)
		}
	}
	close(room.quit)
	log.Printf("room %d closed (idle)", room.id)
}

// saveReplay stores a finished round off the room's tick loop, so a slow disk
// can never stall the simulation.
func (h *Hub) saveReplay(rec replay.Record) {
	go func() {
		if _, err := h.replays.Add(rec); err != nil {
			log.Printf("replay: save failed: %v", err)
		}
	}()
}

func (h *Hub) newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a time-based token; collisions are astronomically
		// unlikely and this path only triggers if the OS RNG fails.
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(b[:])
}

// ServeWS is the HTTP handler that upgrades a connection and runs the client.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		// Browsers attach cookies and connect cross-site freely to
		// WebSockets (no CORS), so any page could otherwise play as the visitor.
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	h.metrics.conns.Inc()
	NewClient(conn).run(h) // blocks until the connection ends
	h.metrics.conns.Dec()
	h.metrics.disconnects.Inc()
}

// originAllowed accepts requests with no Origin header (non-browser clients
// such as cmd/loadtest), same-origin browser pages, and configured origins.
func (h *Hub) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return h.origins[strings.ToLower(strings.TrimRight(origin, "/"))]
}

// Stats is a point-in-time view of the hub for the /stats endpoint.
type Stats struct {
	Rooms   int `json:"rooms"`
	Players int `json:"players"`
}

// ServeStats reports room and player counts as JSON — a minimal operational
// endpoint of the kind a real service would expose to its metrics pipeline.
func (h *Hub) ServeStats(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	rooms := append([]*Room(nil), h.rooms...)
	h.mu.Unlock()

	s := Stats{Rooms: len(rooms)}
	for _, rm := range rooms {
		s.Players += int(atomic.LoadInt32(&rm.count))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}
