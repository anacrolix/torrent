package btprotocol

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/pkg/errors"
)

type Decoder struct {
	R         *bufio.Reader
	Pool      *sync.Pool
	MaxLength Integer // TODO: Should this include the length header or not?
}

// io.EOF is returned if the source terminates cleanly on a message boundary.
// TODO: Is that before or after the message?
func (d *Decoder) Decode(msg *Message) (err error) {
	var length Integer
	err = binary.Read(d.R, binary.BigEndian, &length)
	if err == io.EOF {
		return err
	}

	if err != nil {
		return errors.Wrap(err, "error reading message length:")
	}

	if length > d.MaxLength {
		return errors.New("message too long")
	}

	if length == 0 {
		msg.Keepalive = true
		return nil
	}

	msg.Keepalive = false
	r := &io.LimitedReader{R: d.R, N: int64(length)}
	// Check that all of r was utilized.
	defer func() {
		if err != nil {
			return
		}
		if r.N != 0 {
			err = fmt.Errorf("%d bytes unused in message type %d", r.N, msg.Type)
		}
	}()
	msg.Keepalive = false
	c, err := readByte(r)
	if err != nil {
		return err
	}
	msg.Type = MessageType(c)
	switch msg.Type {
	case Choke, Unchoke, Interested, NotInterested, HaveAll, HaveNone:
		return nil
	case Have, AllowedFast, Suggest:
		return msg.Index.Read(r)
	case Request, Cancel, Reject:
		for _, data := range []*Integer{&msg.Index, &msg.Begin, &msg.Length} {
			err = data.Read(r)
			if err != nil {
				return err
			}
		}
		return nil
	case Bitfield:
		b := make([]byte, length-1)
		_, err = io.ReadFull(r, b)
		msg.Bitfield = unmarshalBitfield(b)
		return err
	case Piece:
		for _, pi := range []*Integer{&msg.Index, &msg.Begin} {
			err := pi.Read(r)
			if err != nil {
				return err
			}
		}
		dataLen := r.N
		msg.Piece = (*d.Pool.Get().(*[]byte))
		if int64(cap(msg.Piece)) < dataLen {
			return errors.New("piece data longer than expected")
		}
		msg.Piece = msg.Piece[:dataLen]
		_, err := io.ReadFull(r, msg.Piece)
		return errors.Wrap(err, "reading piece data")
	case Extended:
		b, err := readByte(r)
		if err != nil {
			return err
		}
		msg.ExtendedID = ExtensionNumber(b)
		msg.ExtendedPayload, err = io.ReadAll(r)
		return err
	case Port:
		return binary.Read(r, binary.BigEndian, &msg.Port)
	default:
		return fmt.Errorf("unknown message type %#v", c)
	}
}

func readByte(r io.Reader) (b byte, err error) {
	var arr [1]byte
	n, err := r.Read(arr[:])
	b = arr[0]
	if n == 1 {
		err = nil
	}
	return b, err
}

func unmarshalBitfield(b []byte) (bf []bool) {
	for _, c := range b {
		for i := 7; i >= 0; i-- {
			bf = append(bf, (c>>uint(i))&1 == 1)
		}
	}
	return
}
