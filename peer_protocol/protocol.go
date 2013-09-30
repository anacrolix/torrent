package peer_protocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type (
	MessageType byte
	Integer     uint32
)

func (i *Integer) Read(r io.Reader) error {
	return binary.Read(r, binary.BigEndian, i)
}

const (
	Protocol = "\x13BitTorrent protocol"
)

const (
	Choke MessageType = iota
	Unchoke
	Interested
	NotInterested
	Have
	Bitfield
	Request
	Piece
	Cancel
)

type Message struct {
	Keepalive            bool
	Type                 MessageType
	Index, Begin, Length Integer
	Piece                []byte
	Bitfield             []bool
}

func (msg Message) MarshalBinary() (data []byte, err error) {
	buf := &bytes.Buffer{}
	if msg.Keepalive {
		data = buf.Bytes()
		return
	}
	err = buf.WriteByte(byte(msg.Type))
	if err != nil {
		return
	}
	switch msg.Type {
	case Choke, Unchoke, Interested, NotInterested:
	case Have:
		err = binary.Write(buf, binary.BigEndian, msg.Index)
	case Request, Cancel:
		for _, i := range []Integer{msg.Index, msg.Begin, msg.Length} {
			err = binary.Write(buf, binary.BigEndian, i)
			if err != nil {
				break
			}
		}
	case Bitfield:
		_, err = buf.Write(marshalBitfield(msg.Bitfield))
	default:
		err = errors.New("unknown message type")
	}
	data = buf.Bytes()
	return
}

type Decoder struct {
	R         *bufio.Reader
	MaxLength Integer
}

func (d *Decoder) Decode(msg *Message) (err error) {
	var length Integer
	err = binary.Read(d.R, binary.BigEndian, &length)
	if err != nil {
		return
	}
	if length > d.MaxLength {
		return errors.New("message too long")
	}
	if length == 0 {
		msg.Keepalive = true
		return
	}
	msg.Keepalive = false
	c, err := d.R.ReadByte()
	if err != nil {
		return
	}
	msg.Type = MessageType(c)
	switch msg.Type {
	case Choke, Unchoke, Interested, NotInterested:
		return
	case Have:
		err = msg.Index.Read(d.R)
	case Request, Cancel:
		err = binary.Read(d.R, binary.BigEndian, []*Integer{&msg.Index, &msg.Begin, &msg.Length})
	case Bitfield:
		b := make([]byte, length-1)
		_, err = io.ReadFull(d.R, b)
		msg.Bitfield = unmarshalBitfield(b)
	default:
		err = fmt.Errorf("unknown message type %#v", c)
	}
	return
}

func encodeMessage(type_ MessageType, data interface{}) []byte {
	w := &bytes.Buffer{}
	w.WriteByte(byte(type_))
	err := binary.Write(w, binary.BigEndian, data)
	if err != nil {
		panic(err)
	}
	return w.Bytes()
}

type Bytes []byte

func (b Bytes) MarshalBinary() ([]byte, error) {
	return b, nil
}

func unmarshalBitfield(b []byte) (bf []bool) {
	for _, c := range b {
		for i := 7; i >= 0; i-- {
			bf = append(bf, (c>>uint(i))&1 == 1)
		}
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
