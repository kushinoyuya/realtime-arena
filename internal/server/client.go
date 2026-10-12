package server

import (
	"log"

	"github.com/kushino/realtime-arena/internal/game"
	"github.com/kushino/realtime-arena/internal/protocol"
	"github.com/kushino/realtime-arena/internal/ws"
)

// sendQueue is the per-client outbound buffer. Snapshots are small and frequent;
// if a client cannot keep up we drop snapshots for it rather than stalling the
// whole room (see Room.broadcast).
const sendQueue = 32

// Client is one connected player. The read side runs in Client.run; the write
// side runs in writePump. The room owns all game state and pushes bytes to this
// client's send channel.
type Client struct {
	conn *ws.Conn
	send chan []byte

	// Assigned once the client is placed into a room.
	id   game.PlayerID
	room *Room
}

// NewClient wraps an upgraded connection.
func NewClient(conn *ws.Conn) *Client {
	return &Client{conn: conn, send: make(chan []byte, sendQueue)}
}

// enqueue tries to queue an outbound message without blocking the caller (the
// room loop). A full queue means a slow client; we drop the message for it.
func (c *Client) enqueue(b []byte) {
	select {
	case c.send <- b:
	default:
		// slow consumer: drop this snapshot
	}
}

// writePump drains the send channel to the socket until the channel is closed.
func (c *Client) writePump() {
	for b := range c.send {
		if err := c.conn.WriteMessage(b); err != nil {
			return
		}
	}
}

// run handles the full client lifecycle: handshake (join/rejoin), then the
// input loop, then cleanup on disconnect. It blocks until the connection ends.
func (c *Client) run(hub *Hub) {
	go c.writePump()
	defer func() {
		if c.room != nil {
			c.room.Leave(c.id)
		}
		close(c.send)
		c.conn.Close()
	}()

	// First message must place the client into a room.
	raw, err := c.conn.ReadMessage()
	if err != nil {
		return
	}
	msg, err := protocol.DecodeClient(raw)
	if err != nil {
		c.enqueue(protocol.MustEncode(protocol.ErrorMsg{Type: protocol.TypeError, Msg: "bad json"}))
		return
	}

	switch msg.Type {
	case protocol.TypeJoin:
		room, id, token := hub.Join(c, sanitizeName(msg.Name))
		c.room, c.id = room, id
		c.enqueue(protocol.MustEncode(protocol.Welcome{
			Type: protocol.TypeWelcome, PlayerID: id, Token: token,
			Width: room.cfg.Width, Height: room.cfg.Height, TickRate: room.cfg.TickRate,
		}))
	case protocol.TypeRejoin:
		room, id, err := hub.Rejoin(c, msg.Token)
		if err != nil {
			c.enqueue(protocol.MustEncode(protocol.ErrorMsg{Type: protocol.TypeError, Msg: "rejoin failed"}))
			return
		}
		c.room, c.id = room, id
		c.enqueue(protocol.MustEncode(protocol.Welcome{
			Type: protocol.TypeWelcome, PlayerID: id, Token: msg.Token,
			Width: room.cfg.Width, Height: room.cfg.Height, TickRate: room.cfg.TickRate,
		}))
	default:
		c.enqueue(protocol.MustEncode(protocol.ErrorMsg{Type: protocol.TypeError, Msg: "expected join"}))
		return
	}

	log.Printf("player %d joined room %d from %s", c.id, c.room.id, c.conn.RemoteAddr())

	// Input loop: translate client intent into room input until the peer goes.
	for {
		raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := protocol.DecodeClient(raw)
		if err != nil {
			continue
		}
		if msg.Type == protocol.TypeInput {
			c.room.Input(c.id, game.ParseDir(msg.Dir))
		}
	}
}

// sanitizeName bounds the display name so one client cannot bloat every
// snapshot with a megabyte of "name".
func sanitizeName(s string) string {
	const max = 16
	out := make([]rune, 0, max)
	for _, r := range s {
		if r == '\n' || r == '\r' {
			continue
		}
		out = append(out, r)
		if len(out) >= max {
			break
		}
	}
	if len(out) == 0 {
		return "anon"
	}
	return string(out)
}
