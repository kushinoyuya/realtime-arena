package ws

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// TestAcceptKey checks our handshake derivation against the worked example in
// RFC 6455 §1.3.
func TestAcceptKey(t *testing.T) {
	got := acceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("acceptKey = %q, want %q", got, want)
	}
}

// maskClientFrame builds a client→server frame (mask bit set) the way a browser
// would, so we can feed it to the reader.
func maskClientFrame(fin bool, opcode byte, payload []byte) []byte {
	var b bytes.Buffer
	b0 := opcode
	if fin {
		b0 |= 0x80
	}
	b.WriteByte(b0)

	n := len(payload)
	switch {
	case n < 126:
		b.WriteByte(0x80 | byte(n)) // mask bit + length
	case n <= 0xFFFF:
		b.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		b.Write(ext[:])
	default:
		b.WriteByte(0x80 | 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		b.Write(ext[:])
	}
	mask := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	b.Write(mask)
	for i, c := range payload {
		b.WriteByte(c ^ mask[i%4])
	}
	return b.Bytes()
}

// fakeConn is a minimal net.Conn backed by in-memory buffers, enough to drive
// the frame codec in tests.
type fakeConn struct {
	out bytes.Buffer
}

// lockedConn records writes like fakeConn but is safe for the concurrent
// writers in TestConcurrentWritesDoNotInterleave; any interleaving it sees comes
// from Conn, not from the test double.
type lockedConn struct {
	fakeConn
	mu sync.Mutex
}

func (l *lockedConn) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.Write(b)
}

func (f *fakeConn) Read(b []byte) (int, error)         { return 0, nil }
func (f *fakeConn) Write(b []byte) (int, error)        { return f.out.Write(b) }
func (f *fakeConn) Close() error                       { return nil }
func (f *fakeConn) LocalAddr() net.Addr                { return nil }
func (f *fakeConn) RemoteAddr() net.Addr               { return nil }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

func TestReadSingleFrame(t *testing.T) {
	payload := []byte("hello arena")
	raw := maskClientFrame(true, opText, payload)
	c := &Conn{br: bufio.NewReader(bytes.NewReader(raw))}
	got, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestReadExtendedLength(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 300) // forces 16-bit extended length
	raw := maskClientFrame(true, opText, payload)
	c := &Conn{br: bufio.NewReader(bytes.NewReader(raw))}
	got, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(payload))
	}
}

func TestFragmentReassembly(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(maskClientFrame(false, opText, []byte("ab")))         // first, not fin
	raw.Write(maskClientFrame(false, opContinuation, []byte("cd"))) // middle
	raw.Write(maskClientFrame(true, opContinuation, []byte("ef")))  // final
	c := &Conn{br: bufio.NewReader(bytes.NewReader(raw.Bytes()))}
	got, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcdef" {
		t.Fatalf("reassembly = %q, want %q", got, "abcdef")
	}
}

func TestPingGetsPong(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(maskClientFrame(true, opPing, []byte("pingdata")))
	raw.Write(maskClientFrame(true, opText, []byte("after")))
	fc := &fakeConn{}
	c := &Conn{raw: fc, br: bufio.NewReader(bytes.NewReader(raw.Bytes()))}

	got, err := c.ReadMessage() // should auto-answer the ping and return "after"
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "after" {
		t.Fatalf("got %q, want %q", got, "after")
	}
	// The written buffer should contain a pong frame (opcode 0xA) echoing data.
	out := fc.out.Bytes()
	if len(out) < 2 || out[0]&0x0F != opPong {
		t.Fatalf("expected a pong frame to be written, got % x", out)
	}
}

func TestWriteFrameRoundTrip(t *testing.T) {
	fc := &fakeConn{}
	c := &Conn{raw: fc}
	if err := c.WriteMessage([]byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	// Decode what we wrote: server frames are unmasked.
	out := fc.out.Bytes()
	if out[0] != (0x80 | opText) {
		t.Fatalf("bad first byte: %x", out[0])
	}
	length := int(out[1] & 0x7F)
	if length != len("snapshot") {
		t.Fatalf("bad length: %d", length)
	}
	if string(out[2:2+length]) != "snapshot" {
		t.Fatalf("bad payload: %q", out[2:2+length])
	}
}

func TestCloseFrameReturnsErrClosed(t *testing.T) {
	raw := maskClientFrame(true, opClose, nil)
	fc := &fakeConn{}
	c := &Conn{raw: fc, br: bufio.NewReader(bytes.NewReader(raw))}
	if _, err := c.ReadMessage(); err != ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

// TestOversizedLengthRejectedBeforeAlloc sends only a header claiming a 2^62
// byte payload. The reader must refuse it up front instead of trying to
// allocate (an unauthenticated memory-exhaustion DoS).
func TestOversizedLengthRejectedBeforeAlloc(t *testing.T) {
	hdr := []byte{0x80 | opText, 0x80 | 127, 0x40, 0, 0, 0, 0, 0, 0, 0, 0xDE, 0xAD, 0xBE, 0xEF}
	c := &Conn{br: bufio.NewReader(bytes.NewReader(hdr))}
	if _, err := c.ReadMessage(); err != ErrTooLarge {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

// TestFragmentedMessageCapped checks that the cap also applies to the
// reassembled message, not only to each fragment.
func TestFragmentedMessageCapped(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), MaxMessageSize/2+1)
	var raw bytes.Buffer
	raw.Write(maskClientFrame(false, opText, chunk))
	raw.Write(maskClientFrame(true, opContinuation, chunk))
	c := &Conn{br: bufio.NewReader(bytes.NewReader(raw.Bytes()))}
	if _, err := c.ReadMessage(); err != ErrTooLarge {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestUnmaskedClientFrameRejected(t *testing.T) {
	raw := []byte{0x80 | opText, 2, 'h', 'i'} // mask bit clear
	c := &Conn{br: bufio.NewReader(bytes.NewReader(raw))}
	if _, err := c.ReadMessage(); err != ErrProtocol {
		t.Fatalf("expected ErrProtocol, got %v", err)
	}
}

// TestConcurrentWritesDoNotInterleave mirrors production: the write pump sends
// messages while the reader answers pings. Every frame on the wire must come
// out whole. (Under -race this also flags any unsynchronised write.)
func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	lc := &lockedConn{}
	c := &Conn{raw: lc}
	payload := bytes.Repeat([]byte("s"), 300) // 16-bit length: header and body are separate writes

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = c.WriteMessage(payload)
				_ = c.writeFrame(opPong, []byte("p"))
			}
		}()
	}
	wg.Wait()

	r := bufio.NewReader(bytes.NewReader(lc.out.Bytes()))
	frames := 0
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(r, hdr[:]); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		n := int(hdr[1] & 0x7F)
		if n == 126 {
			var ext [2]byte
			if _, err := io.ReadFull(r, ext[:]); err != nil {
				t.Fatal(err)
			}
			n = int(binary.BigEndian.Uint16(ext[:]))
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatalf("truncated frame %d: %v", frames, err)
		}
		switch hdr[0] & 0x0F {
		case opText:
			if !bytes.Equal(body, payload) {
				t.Fatalf("frame %d: text payload corrupted", frames)
			}
		case opPong:
			if string(body) != "p" {
				t.Fatalf("frame %d: pong payload corrupted", frames)
			}
		default:
			t.Fatalf("frame %d: unexpected opcode %x (interleaved bytes)", frames, hdr[0])
		}
		frames++
	}
	if frames != 4*200*2 {
		t.Fatalf("decoded %d frames, want %d", frames, 4*200*2)
	}
}
