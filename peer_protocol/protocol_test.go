package peer_protocol

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

func TestBinaryReadSliceOfPointers(t *testing.T) {
	var msg Message
	r := bytes.NewBufferString("\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00@\x00")
	if r.Len() != 12 {
		t.Fatalf("expected 12 bytes left, but there %d", r.Len())
	}
	for _, data := range []*Integer{&msg.Index, &msg.Begin, &msg.Length} {
		err := data.Read(r)
		if err != nil {
			t.Fatal(err)
		}
	}
	if r.Len() != 0 {
		t.FailNow()
	}
}

func TestConstants(t *testing.T) {
	qt.Check(t, qt.Equals(NotInterested, 3))
	qt.Check(t, qt.Equals(HaveAll, 14))
}

func TestBitfieldEncode(t *testing.T) {
	bf := make([]bool, 37)
	bf[2] = true
	bf[7] = true
	bf[32] = true
	s := string(marshalBitfield(bf))
	const expected = "\x21\x00\x00\x00\x80"
	if s != expected {
		t.Fatalf("got %#v, expected %#v", s, expected)
	}
}

func TestBitfieldUnmarshal(t *testing.T) {
	bf := unmarshalBitfield([]byte("\x81\x06"))
	expected := make([]bool, 16)
	expected[0] = true
	expected[7] = true
	expected[13] = true
	expected[14] = true
	if len(bf) != len(expected) {
		t.FailNow()
	}
	for i := range expected {
		if bf[i] != expected[i] {
			t.FailNow()
		}
	}
}

func TestHaveEncode(t *testing.T) {
	actualBytes, err := Message{
		Type:  Have,
		Index: 42,
	}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	actualString := string(actualBytes)
	expected := "\x00\x00\x00\x05\x04\x00\x00\x00\x2a"
	if actualString != expected {
		t.Fatalf("expected %#v, got %#v", expected, actualString)
	}
}

func TestShortRead(t *testing.T) {
	dec := Decoder{
		R:         bufio.NewReader(bytes.NewBufferString("\x00\x00\x00\x02\x00!")),
		MaxLength: 2,
	}
	msg := new(Message)
	err := dec.Decode(msg)
	if !strings.Contains(err.Error(), "1 unused bytes in message type Choke") {
		t.Fatal(err)
	}
}

func TestUnexpectedEOF(t *testing.T) {
	msg := new(Message)
	for _, stream := range []string{
		"\x00\x00\x00",     // Header truncated.
		"\x00\x00\x00\x01", // Expecting 1 more byte.
		// Request with wrong length, and too short anyway.
		"\x00\x00\x00\x06\x06\x00\x00\x00\x00\x00",
		// Request truncated.
		"\x00\x00\x00\x0b\x06\x00\x00\x00\x00\x00",
	} {
		dec := Decoder{
			R:         bufio.NewReader(bytes.NewBufferString(stream)),
			MaxLength: 42,
		}
		err := dec.Decode(msg)
		if err == nil {
			t.Fatalf("expected an error decoding %q", stream)
		}
	}
}

func TestMarshalKeepalive(t *testing.T) {
	b, err := (Message{
		Keepalive: true,
	}).MarshalBinary()
	if err != nil {
		t.Fatalf("error marshalling keepalive: %s", err)
	}
	bs := string(b)
	const expected = "\x00\x00\x00\x00"
	if bs != expected {
		t.Fatalf("marshalled keepalive is %q, expected %q", bs, expected)
	}
}

func TestMarshalPortMsg(t *testing.T) {
	b, err := (Message{
		Type: Port,
		Port: 0xaabb,
	}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "\x00\x00\x00\x03\x09\xaa\xbb" {
		t.FailNow()
	}
}

func TestUnmarshalPortMsg(t *testing.T) {
	var m Message
	d := Decoder{
		R:         bufio.NewReader(bytes.NewBufferString("\x00\x00\x00\x03\x09\xaa\xbb")),
		MaxLength: 8,
	}
	err := d.Decode(&m)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != Port {
		t.FailNow()
	}
	if m.Port != 0xaabb {
		t.FailNow()
	}
}

// Writes a limited number of bytes, then fails.
type failingMessageWriter struct {
	left int
}

func (me *failingMessageWriter) WriteByte(byte) error {
	if me.left == 0 {
		return io.ErrShortWrite
	}
	me.left--
	return nil
}

func (me *failingMessageWriter) Write(b []byte) (int, error) {
	if len(b) > me.left {
		return 0, io.ErrShortWrite
	}
	me.left -= len(b)
	return len(b), nil
}

// A writer error while writing a piece message's data is returned.
func TestPieceMessageWriteToBodyError(t *testing.T) {
	msg := Message{Type: Piece, Index: 1, Begin: 2, Piece: []byte("data")}
	// The length prefix, type and index and begin fit; the data doesn't.
	err := msg.WriteTo(&failingMessageWriter{left: 4 + 1 + 8})
	qt.Check(t, qt.ErrorIs(err, io.ErrShortWrite))
}

// Records writes, and only gives its handshake to read once something has been written, as a
// peer that waits for ours first would.
type handshakeTestConn struct {
	mu     sync.Mutex
	writes [][]byte
	wrote  chan struct{}
	r      io.Reader
}

func (me *handshakeTestConn) Write(b []byte) (int, error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if len(me.writes) == 0 {
		close(me.wrote)
	}
	me.writes = append(me.writes, bytes.Clone(b))
	return len(b), nil
}

// Blocks until something is written, so a handshake that waits to read before writing never
// completes.
func (me *handshakeTestConn) Read(b []byte) (int, error) {
	<-me.wrote
	return me.r.Read(b)
}

// The handshake is written in as few writes as the order of the exchange allows, and the bytes are
// unchanged.
func TestHandshakeWrites(t *testing.T) {
	ext := NewPeerExtensionBytes(ExtensionBitFast, ExtensionBitLtep)
	peerExt := NewPeerExtensionBytes(ExtensionBitDht)
	ih := metainfo.Hash{1, 2, 3}
	ourID := [20]byte{'o', 'u', 'r', 's'}
	theirID := [20]byte{'t', 'h', 'e', 'i', 'r', 's'}
	theirs := slices.Concat([]byte(Protocol), peerExt[:], ih[:], theirID[:])
	for _, outgoing := range []bool{true, false} {
		conn := &handshakeTestConn{wrote: make(chan struct{}), r: bytes.NewReader(theirs)}
		var knownIh *metainfo.Hash
		if outgoing {
			knownIh = &ih
		}
		res, err := Handshake(context.Background(), conn, knownIh, ourID, ext)
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.Equals(res.Hash, ih))
		qt.Check(t, qt.Equals(res.PeerID, theirID))
		qt.Check(t, qt.Equals(res.PeerExtensionBits, peerExt))
		qt.Check(t, qt.DeepEquals(
			slices.Concat(conn.writes...),
			slices.Concat([]byte(Protocol), ext[:], ih[:], ourID[:])))
		wantWrites := 2
		if outgoing {
			wantWrites = 1
		}
		qt.Check(t, qt.HasLen(conn.writes, wantWrites), qt.Commentf("outgoing=%v", outgoing))
	}
}

type countingMessageWriter struct{ n int }

func (me *countingMessageWriter) WriteByte(byte) error { me.n++; return nil }

func (me *countingMessageWriter) Write(b []byte) (int, error) {
	me.n += len(b)
	return len(b), nil
}

// The payload length written in the message prefix matches what's written for every message type.
func TestMessagePayloadLength(t *testing.T) {
	for _, msg := range []Message{
		{Keepalive: true},
		{Type: Choke}, {Type: Unchoke}, {Type: Interested}, {Type: NotInterested},
		{Type: HaveAll}, {Type: HaveNone},
		{Type: Have, Index: 1}, {Type: AllowedFast, Index: 2}, {Type: Suggest, Index: 3},
		{Type: Request, Index: 1, Begin: 2, Length: 3}, {Type: Cancel}, {Type: Reject},
		{Type: Bitfield}, {Type: Bitfield, Bitfield: make([]bool, 1)},
		{Type: Bitfield, Bitfield: make([]bool, 8)}, {Type: Bitfield, Bitfield: make([]bool, 9)},
		{Type: Piece}, {Type: Piece, Piece: make([]byte, 1<<14)},
		{Type: Extended}, {Type: Extended, ExtendedID: 3, ExtendedPayload: []byte("d1:ai1ee")},
		{Type: Port, Port: 6881},
		{Type: HashRequest, Length: 2}, {Type: HashReject, Length: 2},
		{Type: Hashes}, {Type: Hashes, Hashes: make([][32]byte, 3)},
	} {
		var c countingMessageWriter
		qt.Assert(t, qt.IsNil(msg.writePayloadTo(&c)))
		l, err := msg.getPayloadLength()
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.Equals(int(l), c.n), qt.Commentf("%v", msg.Type))
		b, err := msg.MarshalBinary()
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.HasLen(b, 4+c.n))
	}
	// An unknown type fails before anything is written.
	var w bytes.Buffer
	qt.Check(t, qt.IsNotNil((&Message{Type: 99}).WriteTo(&w)))
	qt.Check(t, qt.Equals(w.Len(), 0))
}

// Every message type, with a distinct value in each field it carries, decodes back to itself.
func TestMessageRoundTrip(t *testing.T) {
	bitfield := make([]bool, 16)
	bitfield[0], bitfield[9], bitfield[15] = true, true, true
	root := [32]byte{9, 8, 7}
	for _, msg := range []Message{
		{Keepalive: true},
		{Type: Choke}, {Type: Unchoke}, {Type: Interested}, {Type: NotInterested},
		{Type: HaveAll}, {Type: HaveNone},
		{Type: Have, Index: 0x01020304}, {Type: AllowedFast, Index: 5}, {Type: Suggest, Index: 6},
		{Type: Request, Index: 1, Begin: 0x4000, Length: 0x4000},
		{Type: Cancel, Index: 2, Begin: 3, Length: 4}, {Type: Reject, Index: 5, Begin: 6, Length: 7},
		{Type: Bitfield, Bitfield: bitfield},
		{Type: Piece, Index: 1, Begin: 2, Piece: []byte("data")},
		{Type: Extended, ExtendedID: 3, ExtendedPayload: []byte("d1:ai1ee")},
		{Type: Port, Port: 6881},
		{Type: HashRequest, PiecesRoot: root, BaseLayer: 1, Index: 2, Length: 3, ProofLayers: 4},
		{Type: HashReject, PiecesRoot: root, BaseLayer: 5, Index: 6, Length: 7, ProofLayers: 8},
		{
			Type: Hashes, PiecesRoot: root, BaseLayer: 1, Index: 2, Length: 3, ProofLayers: 4,
			Hashes: [][32]byte{{1}, {2}, {3}},
		},
	} {
		d := Decoder{R: bufio.NewReader(bytes.NewReader(msg.MustMarshalBinary())), MaxLength: 1 << 10}
		var got Message
		qt.Assert(t, qt.IsNil(d.Decode(&got)), qt.Commentf("%v", msg.Type))
		qt.Check(t, qt.DeepEquals(got, msg))
	}
}

// Fails writes that would take it past limit bytes.
type limitedMessageWriter struct {
	bytes.Buffer
	limit int
}

func (me *limitedMessageWriter) WriteByte(c byte) error {
	if me.Len() >= me.limit {
		return io.ErrShortWrite
	}
	return me.Buffer.WriteByte(c)
}

func (me *limitedMessageWriter) Write(b []byte) (int, error) {
	if me.Len()+len(b) > me.limit {
		return 0, io.ErrShortWrite
	}
	return me.Buffer.Write(b)
}

// A message of any type stopped by its writer at any byte returns the writer's error.
func TestMessageWriteToWriterError(t *testing.T) {
	for _, msg := range []Message{
		{Keepalive: true},
		{Type: Choke}, {Type: Unchoke}, {Type: Interested}, {Type: NotInterested},
		{Type: HaveAll}, {Type: HaveNone},
		{Type: Have, Index: 1}, {Type: AllowedFast, Index: 1}, {Type: Suggest, Index: 1},
		{Type: Request, Index: 1, Begin: 2, Length: 3}, {Type: Cancel, Index: 1, Begin: 2, Length: 3},
		{Type: Reject, Index: 1, Begin: 2, Length: 3},
		{Type: Bitfield, Bitfield: make([]bool, 9)},
		{Type: Piece, Index: 1, Begin: 2, Piece: []byte("data")},
		{Type: Extended, ExtendedID: 3, ExtendedPayload: []byte("d1:ai1ee")},
		{Type: Port, Port: 6881},
		{Type: HashRequest, Index: 1, Length: 2}, {Type: HashReject, Index: 1, Length: 2},
		{Type: Hashes, Index: 1, Length: 2, Hashes: make([][32]byte, 2)},
	} {
		full := msg.MustMarshalBinary()
		for limit := range len(full) {
			w := limitedMessageWriter{limit: limit}
			qt.Check(t, qt.ErrorIs(msg.WriteTo(&w), io.ErrShortWrite), qt.Commentf("%v limit %v", msg.Type, limit))
		}
		w := limitedMessageWriter{limit: len(full)}
		qt.Check(t, qt.IsNil(msg.WriteTo(&w)))
		qt.Check(t, qt.DeepEquals(w.Bytes(), full))
	}
}

func TestUnmarshalBitfield(t *testing.T) {
	qt.Check(t, qt.IsNil(unmarshalBitfield(nil)))
	qt.Check(t, qt.IsNil(unmarshalBitfield([]byte{})))
	bf := unmarshalBitfield([]byte{0x80, 0x01, 0xff})
	qt.Check(t, qt.DeepEquals(bf, []bool{
		true, false, false, false, false, false, false, false,
		false, false, false, false, false, false, false, true,
		true, true, true, true, true, true, true, true,
	}))
	// Allocated once at its full size, rather than grown by appending.
	qt.Check(t, qt.Equals(cap(bf), 24))
}
