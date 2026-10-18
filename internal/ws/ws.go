// Package ws is a small, dependency-free WebSocket (RFC 6455) server built on
// the Go standard library alone. It implements exactly what this project needs
// — the upgrade handshake, masked client→server and unmasked server→client
// text frames, fragmentation reassembly, and ping/pong/close control frames.
// It is intentionally not a general-purpose library; keeping it in-tree means
// the whole project builds and runs with zero external modules.
package ws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// guid is the fixed magic value from RFC 6455 §1.3 used to derive the accept key.
const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// MaxMessageSize bounds a single frame and a reassembled message. Game
// messages are tiny; anything larger is a misbehaving or hostile peer, and
// without a cap a single header claiming a 2^63-byte payload would make the
// reader allocate unbounded memory.
const MaxMessageSize = 4096

var (
	// ErrClosed is returned by ReadMessage when the peer sent a close frame.
	ErrClosed = errors.New("ws: connection closed by peer")
	// ErrTooLarge is returned when a frame or message exceeds MaxMessageSize.
	ErrTooLarge = errors.New("ws: message too large")
	// ErrProtocol is returned for frames that violate RFC 6455.
	ErrProtocol = errors.New("ws: protocol error")
)

// Conn is an upgraded WebSocket connection. One goroutine may read while any
// number write: writes are serialised by wmu, because the reader itself also
// writes (it answers pings and echoes close frames) concurrently with the
// server's write pump.
type Conn struct {
	raw    net.Conn
	br     *bufio.Reader
	client bool // true for the dialling side: outgoing frames must be masked

	// readTimeout, if set, is re-armed before every frame read, so any frame
	// from the peer (including a pong that ReadMessage swallows) proves it is
	// alive.
	readTimeout time.Duration

	wmu sync.Mutex
}

// acceptKey derives the Sec-WebSocket-Accept response value from the client's
// Sec-WebSocket-Key per RFC 6455.
func acceptKey(key string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, key+guid)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Upgrade performs the server-side handshake and hijacks the TCP connection.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("ws: not a websocket upgrade request")
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, errors.New("ws: missing Connection: upgrade")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("ws: missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("ws: response writer does not support hijacking")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		conn.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{raw: conn, br: brw.Reader}, nil
}

// Dial opens a client WebSocket connection to a ws:// URL. It is used by the
// load tester to drive the server with many concurrent bots. (TLS/wss is out
// of scope for this local tool.)
func Dial(rawurl string) (*Conn, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, errors.New("ws: Dial supports ws:// only")
	}
	host := u.Host
	if u.Port() == "" {
		host = host + ":80"
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	var kb [16]byte
	if _, err := rand.Read(kb[:]); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(kb[:])

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, errors.New("ws: unexpected status " + resp.Status)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		conn.Close()
		return nil, errors.New("ws: bad Sec-WebSocket-Accept")
	}
	// Do not read resp.Body: it is backed by br, which now holds the frame
	// stream. Reuse br directly.
	return &Conn{raw: conn, br: br, client: true}, nil
}

// frame is a single decoded WebSocket frame.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// readFrame decodes one frame from the reader, unmasking the payload if the
// client set the mask bit (which browsers always do).
func (c *Conn) readFrame() (frame, error) {
	if c.readTimeout > 0 {
		if err := c.raw.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			return frame{}, err
		}
	}
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return frame{}, err
	}
	fin := hdr[0]&0x80 != 0
	opcode := hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7F)

	// RFC 6455 §5.1: every client→server frame must be masked, and the server
	// must fail the connection otherwise.
	if !c.client && !masked {
		return frame{}, ErrProtocol
	}

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return frame{}, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	// Check before allocating; kept in uint64 so a huge length cannot wrap
	// negative when converted to int.
	if length > MaxMessageSize {
		return frame{}, ErrTooLarge
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return frame{}, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return frame{}, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return frame{fin: fin, opcode: opcode, payload: payload}, nil
}

// writeFrame writes a single frame. Server→client frames are unmasked;
// client→server frames (c.client) must be masked with a fresh random key per
// RFC 6455 §5.3.
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	b0 := byte(0x80) | opcode // FIN set: we never fragment outgoing messages
	n := len(payload)

	var hdr []byte
	switch {
	case n < 126:
		hdr = []byte{b0, byte(n)}
	case n <= 0xFFFF:
		hdr = []byte{b0, 126, 0, 0}
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0] = b0
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}

	if !c.client {
		if _, err := c.raw.Write(hdr); err != nil {
			return err
		}
		_, err := c.raw.Write(payload)
		return err
	}

	// Client side: set the mask bit, append a random 4-byte key, send masked.
	hdr[1] |= 0x80
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ key[i%4]
	}
	if _, err := c.raw.Write(hdr); err != nil {
		return err
	}
	if _, err := c.raw.Write(key[:]); err != nil {
		return err
	}
	_, err := c.raw.Write(masked)
	return err
}

// ReadMessage returns the next complete text or binary message. It transparently
// answers pings, discards pongs, reassembles fragmented messages, and returns
// ErrClosed on a close frame.
func (c *Conn) ReadMessage() ([]byte, error) {
	var buf []byte
	for {
		f, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch f.opcode {
		case opPing:
			if err := c.writeFrame(opPong, f.payload); err != nil {
				return nil, err
			}
		case opPong:
			// ignore
		case opClose:
			_ = c.writeFrame(opClose, nil)
			return nil, ErrClosed
		case opText, opBinary, opContinuation:
			if len(buf)+len(f.payload) > MaxMessageSize {
				return nil, ErrTooLarge
			}
			buf = append(buf, f.payload...)
			if f.fin {
				return buf, nil
			}
		}
	}
}

// WriteMessage sends a text message.
func (c *Conn) WriteMessage(data []byte) error { return c.writeFrame(opText, data) }

// Ping sends a ping control frame.
func (c *Conn) Ping() error { return c.writeFrame(opPing, nil) }

// Close sends a close frame (best effort) and closes the TCP connection.
func (c *Conn) Close() error {
	_ = c.writeFrame(opClose, nil)
	return c.raw.Close()
}

// SetReadDeadline sets the read deadline on the underlying connection, used to
// detect dead peers.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }

// SetReadTimeout makes every frame read fail if the peer stays silent for d.
// Pair it with periodic Ping calls so idle-but-alive peers keep answering.
// Must be called before reading starts.
func (c *Conn) SetReadTimeout(d time.Duration) { c.readTimeout = d }

// SetWriteDeadline bounds how long a write may block, so a peer that stops
// reading cannot wedge the write pump forever.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.raw.SetWriteDeadline(t) }

// RemoteAddr reports the peer address, for logging.
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }
