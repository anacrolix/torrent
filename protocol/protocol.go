package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	// "errors"
	"fmt"
	"io"
	"io/ioutil"
	// "os"
)

type (
	MessageType byte
	Integer     uint32
	PieceIndex  Integer
	PieceOffset Integer
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
	KeepAlive bool
	Type      MessageType
	Bitfield  []bool
	Index     PieceIndex
	Begin     PieceOffset
	Length    Integer
	Piece     []byte
}

func (msg *Message) UnmarshalReader(r io.Reader) (err error) {
	err = binary.Read(r, binary.BigEndian, &msg.Type)
	switch err {
	case nil:
		msg.KeepAlive = false
	case io.EOF:
		msg.KeepAlive = true
		err = nil
		return
	default:
		return
	}
	switch msg.Type {
	case Choke, Unchoke, Interested, NotInterested:
	case Have:
		err = binary.Read(r, binary.BigEndian, &msg.Index)
	case Request, Cancel:
		err = binary.Read(r, binary.BigEndian, &msg.Index)
		if err != nil {
			return
		}
		err = binary.Read(r, binary.BigEndian, &msg.Begin)
		if err != nil {
			return
		}
		err = binary.Read(r, binary.BigEndian, &msg.Length)
	case Bitfield:
		// var bf []byte
		_, err = ioutil.ReadAll(r)
		if err != nil {
			return
		}
	case Piece:
	default:
		return fmt.Errorf("unknown type: %v", msg.Type)
	}
	return
}

func (msg *Message) MarshalBinary() (b []byte, err error) {
	w := &bytes.Buffer{}
	if msg.KeepAlive {
		b = w.Bytes()
		return
	}
	err = w.WriteByte(byte(msg.Type))
	if err != nil {
		return
	}
	switch msg.Type {
	case Choke, Unchoke, Interested, NotInterested:
	case Have:
		err = binary.Write(w, binary.BigEndian, msg.Index)
	case Request, Cancel:
		err = binary.Write(w, binary.BigEndian, msg.Index)
		if err != nil {
			return
		}
		err = binary.Write(w, binary.BigEndian, msg.Begin)
		if err != nil {
			return
		}
		err = binary.Write(w, binary.BigEndian, msg.Length)
	case Bitfield, Piece:
		panic("unimplemented")
	default:
		err = errors.New("unknown type")
		return
	}
	if err == nil {
		b = w.Bytes()
	}
	return
}
