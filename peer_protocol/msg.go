package peer_protocol

import (
	"bufio"
	"bytes"
	"encoding"
	"encoding/binary"
	"fmt"
	"io"
)

// This is a lazy union representing all the possible fields for messages. Go doesn't have ADTs, and
// I didn't choose to use type-assertions. Fields are ordered to minimize struct size and padding.
type Message struct {
	PiecesRoot           [32]byte
	Piece                []byte
	Bitfield             []bool
	ExtendedPayload      []byte
	Hashes               [][32]byte
	Index, Begin, Length Integer
	BaseLayer            Integer
	ProofLayers          Integer
	Port                 uint16
	Type                 MessageType
	ExtendedID           ExtensionNumber
	Keepalive            bool
}

var _ interface {
	encoding.BinaryUnmarshaler
	encoding.BinaryMarshaler
} = (*Message)(nil)

func MakeCancelMessage(piece, offset, length Integer) Message {
	return Message{
		Type:   Cancel,
		Index:  piece,
		Begin:  offset,
		Length: length,
	}
}

func (msg Message) RequestSpec() (ret RequestSpec) {
	return RequestSpec{
		msg.Index,
		msg.Begin,
		func() Integer {
			if msg.Type == Piece {
				return Integer(len(msg.Piece))
			} else {
				return msg.Length
			}
		}(),
	}
}

func (msg Message) MustMarshalBinary() []byte {
	b, err := msg.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return b
}

func (msg Message) WriteTo(w io.Writer) (n int64, err error) {
	dw := newDataWriter(w)
	defer func() {
		n = dw.GetBytesWritten()
	}()

	err = dw.WriteByte(byte(msg.Type))
	if err != nil {
		return
	}

	switch msg.Type {
	case Choke, Unchoke, Interested, NotInterested, HaveAll, HaveNone:
	case Have, AllowedFast, Suggest:
		err = dw.BinaryWrite(binary.BigEndian, msg.Index)
	case Request, Cancel, Reject:
		for _, i := range []Integer{msg.Index, msg.Begin, msg.Length} {
			err = dw.BinaryWrite(binary.BigEndian, i)
			if err != nil {
				break
			}
		}
	case Bitfield:
		_, err = dw.Write(marshalBitfield(msg.Bitfield))
	case Piece:
		for _, i := range []Integer{msg.Index, msg.Begin} {
			err = dw.BinaryWrite(binary.BigEndian, i)
			if err != nil {
				return
			}
		}
		written, err := dw.Write(msg.Piece)
		if err != nil {
			break
		}
		if written != len(msg.Piece) {
			panic(written)
		}
	case Extended:
		err = dw.WriteByte(byte(msg.ExtendedID))
		if err != nil {
			return
		}
		_, err = dw.Write(msg.ExtendedPayload)
	case Port:
		err = dw.BinaryWrite(binary.BigEndian, msg.Port)
	default:
		err = fmt.Errorf("unknown message type: %v", msg.Type)
	}
	return
}

const (
	msgTypeLen       = 1 // byte
	msgIndexLen      = 4 // uint32
	msgBeginLen      = 4 // uint32
	msgExtendedIDLen = 1 // byte
	msgPortLen       = 2 // uint16
)

func (msg Message) GetDataLength() (length int, err error) {
	if !msg.Keepalive {
		length += msgTypeLen
		switch msg.Type {
		case Choke, Unchoke, Interested, NotInterested, HaveAll, HaveNone:
		case Have, AllowedFast, Suggest:
			length += msgIndexLen
		case Request, Cancel, Reject:
			length += msgIndexLen + msgBeginLen + msgBeginLen
		case Bitfield:
			length += (len(msg.Bitfield) + 7) / 8
		case Piece:
			length += msgIndexLen + msgBeginLen + len(msg.Piece)
		case Extended:
			length += msgExtendedIDLen + len(msg.ExtendedPayload)
		case Port:
			length += msgPortLen
		default:
			err = fmt.Errorf("unknown message type: %v", msg.Type)
		}
	}
	return
}

func (msg Message) MarshalBinary() (data []byte, err error) {
	// It might look like you could have a pool of buffers and preallocate the message length
	// prefix, but because we have to return []byte, it becomes non-trivial to make this fast. You
	// will need a benchmark.
	var buf bytes.Buffer
	if !msg.Keepalive {
		_, err = msg.WriteTo(&buf)
	}
	data = make([]byte, 4+buf.Len())
	binary.BigEndian.PutUint32(data, uint32(buf.Len()))
	if buf.Len() != copy(data[4:], buf.Bytes()) {
		panic("bad copy")
	}
	return
}

func marshalBitfield(bf []bool) (b []byte) {
	b = make([]byte, (len(bf)+7)/8)
	for i, have := range bf {
		if !have {
			continue
		}
		c := b[i/8]
		c |= 1 << uint(7-i%8)
		b[i/8] = c
	}
	return
}

func (me *Message) UnmarshalBinary(b []byte) error {
	d := Decoder{
		R: bufio.NewReader(bytes.NewReader(b)),
	}
	err := d.Decode(me)
	if err != nil {
		return err
	}
	if d.R.Buffered() != 0 {
		return fmt.Errorf("%d trailing bytes", d.R.Buffered())
	}
	return nil
}

type dataWriter struct {
	writer io.Writer
	n      int64
}

func (d *dataWriter) BinaryWrite(order binary.ByteOrder, data any) error {
	err := binary.Write(d.writer, order, data)
	if err != nil {
		return err
	}
	d.n += int64(binary.Size(data))
	return nil
}

func (d *dataWriter) Write(bytes []byte) (int, error) {
	n, err := d.writer.Write(bytes)
	if err != nil {
		return n, err
	}
	d.n += int64(n)
	return n, nil
}

func (d *dataWriter) WriteByte(b byte) error {
	n, err := d.Write([]byte{b})
	if err != nil {
		return err
	}
	d.n += int64(n)
	return nil
}

func (d *dataWriter) GetBytesWritten() int64 {
	return d.n
}

func newDataWriter(writer io.Writer) *dataWriter {
	return &dataWriter{writer, 0}
}
