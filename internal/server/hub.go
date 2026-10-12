package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/ws"
)

// Hub performs matchmaking: it places each joining connection into an open room
// (creating one when all rooms are full) and keeps the reconnect-token registry.
// Rooms themselves are self-contained goroutines; the hub only routes.
type Hub struct {
	mu         sync.Mutex
	rooms      []*Room
	tokens     map[string]tokenInfo
	cap        int32
	nextRoomID int
	seedSeq    int64
	width      int
	height     int
	tickRate   int
}

type tokenInfo struct {
	room *Room
	id   game.PlayerID
}

// HubConfig configures matchmaking and the room ruleset.
type HubConfig struct {
	Capacity int // players per room
	Width    int
	Height   int
	TickRate int
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
	return &Hub{
		tokens:   make(map[string]tokenInfo),
		cap:      int32(cfg.Capacity),
		width:    cfg.Width,
		height:   cfg.Height,
		tickRate: cfg.TickRate,
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
		room = newRoom(h.nextRoomID, cfg)
		h.nextRoomID++
		h.rooms = append(h.rooms, room)
	}
	atomic.AddInt32(&room.count, 1) // reserve the slot before we release the lock
	token := h.newToken()
	h.mu.Unlock()

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
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	NewClient(conn).run(h)
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
