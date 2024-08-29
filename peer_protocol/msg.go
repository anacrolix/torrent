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

type MessageWriter interface {
	io.ByteWriter
	io.Writer
}

func (msg *Message) writeHashCommon(buf MessageWriter) (err error) {
	if _, err = buf.Write(msg.PiecesRoot[:]); err != nil {
		return
	}
	for _, d := range []Integer{msg.BaseLayer, msg.Index, msg.Length, msg.ProofLayers} {
		if err = binary.Write(buf, binary.BigEndian, d); err != nil {
			return
		}
	}
	return nil
}

func (msg *Message) writePayloadTo(buf MessageWriter) (err error) {
	if !msg.Keepalive {
		err = buf.WriteByte(byte(msg.Type))
		if err != nil {
			return
		}
		switch msg.Type {
		case Choke, Unchoke, Interested, NotInterested, HaveAll, HaveNone:
		case Have, AllowedFast, Suggest:
			err = binary.Write(buf, binary.BigEndian, msg.Index)
		case Request, Cancel, Reject:
			for _, i := range []Integer{msg.Index, msg.Begin, msg.Length} {
				err = binary.Write(buf, binary.BigEndian, i)
				if err != nil {
					break
				}
			}
		case Bitfield:
			_, err = buf.Write(marshalBitfield(msg.Bitfield))
		case Piece:
			for _, i := range []Integer{msg.Index, msg.Begin} {
				err = binary.Write(buf, binary.BigEndian, i)
				if err != nil {
					return
				}
			}
			n, err := buf.Write(msg.Piece)
			if err != nil {
				break
			}
			if n != len(msg.Piece) {
				panic(n)
			}
		case Extended:
			err = buf.WriteByte(byte(msg.ExtendedID))
			if err != nil {
				return
			}
			_, err = buf.Write(msg.ExtendedPayload)
		case Port:
			err = binary.Write(buf, binary.BigEndian, msg.Port)
		case HashRequest, HashReject:
			err = msg.writeHashCommon(buf)
		case Hashes:
			err = msg.writeHashCommon(buf)
			if err != nil {
				return
			}
			for _, h := range msg.Hashes {
				if _, err = buf.Write(h[:]); err != nil {
					return
				}
			}
		default:
			err = fmt.Errorf("unknown message type: %v", msg.Type)
		}
	}
	return
}

func (msg *Message) WriteTo(w MessageWriter) (err error) {
	length, err := msg.getPayloadLength()
	if err != nil {
		return
	}
	err = binary.Write(w, binary.BigEndian, length)
	if err != nil {
		return
	}
	return msg.writePayloadTo(w)
}

func (msg *Message) getPayloadLength() (length Integer, err error) {
	var lw lengthWriter
	err = msg.writePayloadTo(&lw)
	length = lw.n
	return
}

func (msg Message) MarshalBinary() (data []byte, err error) {
	// It might look like you could have a pool of buffers and preallocate the message length
	// prefix, but because we have to return []byte, it becomes non-trivial to make this fast. You
	// will need a benchmark.
	var buf bytes.Buffer
	err = msg.WriteTo(&buf)
	data = buf.Bytes()
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

type lengthWriter struct {
	n Integer
}

func (l *lengthWriter) WriteByte(c byte) error {
	l.n++
	return nil
}

func (l *lengthWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	l.n += Integer(n)
	return
}
