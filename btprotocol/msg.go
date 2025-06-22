package btprotocol

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/james-lawrence/torrent/internal/x/bitmapx"
)

// This is a lazy union representing all the possible fields for messages. Go doesn't have ADTs, and
// I didn't choose to use type-assertions.
type Message struct {
	Keepalive            bool
	Type                 MessageType
	Index, Begin, Length Integer
	Piece                []byte
	Bitfield             []bool
	ExtendedID           ExtensionNumber
	ExtendedPayload      []byte
	Port                 uint16
}

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

func (msg Message) MarshalBinary() (data []byte, err error) {
	buf := &bytes.Buffer{}
	if !msg.Keepalive {
		err = buf.WriteByte(byte(msg.Type))
		if err != nil {
			return nil, err
		}
		switch msg.Type {
		case Choke, Unchoke, Interested, NotInterested, HaveAll, HaveNone:
		case Have, AllowedFast, Suggest:
			if err = binary.Write(buf, binary.BigEndian, msg.Index); err != nil {
				return nil, err
			}
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
		default:
			err = fmt.Errorf("unknown message type: %v", msg.Type)
		}
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

func NewAllowedFast(piece uint32) Message {
	return Message{
		Type:  AllowedFast,
		Index: Integer(piece),
	}
}

func NewKeepAlive() Message {
	return Message{
		Keepalive: true,
	}
}

func NewExtendedHandshake(encoded []byte) Message {
	return NewExtended(HandshakeExtendedID, encoded)
}

func NewExtended(id ExtensionNumber, encoded []byte) Message {
	return Message{
		Type:            Extended,
		ExtendedID:      id,
		ExtendedPayload: encoded,
	}
}

func NewHaveNone() Message {
	return Message{Type: HaveNone}
}

func NewHaveAll() Message {
	return Message{Type: HaveAll}
}

func NewBitField(n uint64, b *roaring.Bitmap) Message {
	return Message{
		Type:     Bitfield,
		Bitfield: bitmapx.Bools(int(n), b),
	}
}

func NewInterested(b bool) Message {
	i := NotInterested
	if b {
		i = Interested
	}
	return Message{
		Type: i,
	}
}

func NewChoked() Message {
	return Message{
		Type: Choke,
	}
}

func NewUnchoked() Message {
	return Message{
		Type: Unchoke,
	}
}

func NewPort(p uint16) Message {
	return Message{
		Type: Port,
		Port: p,
	}
}

func NewDHTPort(p uint16) Message {
	return Message{
		Type: Port,
		Port: p,
	}
}

func NewPiece(index Integer, begin Integer, bin []byte) Message {
	return Message{
		Type:  Piece,
		Index: index,
		Begin: begin,
		Piece: bin,
	}
}

func NewHavePiece(p uint64) Message {
	return Message{
		Type:  Have,
		Index: Integer(p),
	}
}
