// Package protocol defines the JSON messages exchanged over the WebSocket. The
// client only ever sends intent (join / input); it never sends positions. All
// authority lives on the server, which answers with periodic snapshots. This
// separation is the whole point of an authoritative server: a malicious or
// buggy client cannot move anyone, only request a move the server may reject.
package protocol

import (
	"encoding/json"

	"github.com/kushino/realtime-arena/internal/game"
)

// Message type tags (the "type" field on every frame).
const (
	TypeJoin     = "join"     // client -> server
	TypeInput    = "input"    // client -> server
	TypeRejoin   = "rejoin"   // client -> server
	TypeWelcome  = "welcome"  // server -> client
	TypeSnapshot = "snapshot" // server -> client
	TypeError    = "error"    // server -> client
)

// ClientMsg is the union of everything a client may send. Unused fields stay at
// their zero values; Type selects the meaning.
type ClientMsg struct {
	Type  string `json:"type"`
	Name  string `json:"name,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Token string `json:"token,omitempty"`
}

// DecodeClient parses a raw client frame. Invalid JSON yields an error the
// caller can turn into a protocol error reply rather than a disconnect.
func DecodeClient(b []byte) (ClientMsg, error) {
	var m ClientMsg
	err := json.Unmarshal(b, &m)
	return m, err
}

// Welcome is sent once, right after a successful join. The token lets the
// client reclaim its slot after a dropped connection (see TypeRejoin).
type Welcome struct {
	Type     string        `json:"type"`
	PlayerID game.PlayerID `json:"playerId"`
	Token    string        `json:"token"`
	Width    int           `json:"width"`
	Height   int           `json:"height"`
	TickRate int           `json:"tickRate"`
}

// SnakeView is one snake as the client needs to draw it.
type SnakeView struct {
	ID    game.PlayerID `json:"id"`
	Name  string        `json:"name"`
	Body  []game.Point  `json:"body"`
	Alive bool          `json:"alive"`
	Score int           `json:"score"`
}

// Snapshot is the authoritative world state at one tick. ServerTimeMs lets the
// client (and the load tester) measure end-to-end latency.
type Snapshot struct {
	Type         string        `json:"type"`
	Tick         int           `json:"tick"`
	ServerTimeMs int64         `json:"serverTimeMs"`
	You          game.PlayerID `json:"you"`
	Snakes       []SnakeView   `json:"snakes"`
	Food         []game.Point  `json:"food"`
}

// ErrorMsg reports a rejected request without tearing the connection down.
type ErrorMsg struct {
	Type string `json:"type"`
	Msg  string `json:"msg"`
}

// Encode marshals any server message to bytes.
func Encode(v interface{}) ([]byte, error) { return json.Marshal(v) }

// MustEncode is Encode for messages that cannot fail to marshal (fixed shapes).
func MustEncode(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Our server messages are simple structs; a failure here is a bug.
		panic("protocol: encode: " + err.Error())
	}
	return b
}
