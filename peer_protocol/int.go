package peer_protocol

import (
	"encoding/binary"
	"io"

	"github.com/pkg/errors"
)

type Integer uint32

func (i *Integer) UnmarshalBinary(b []byte) error {
	if len(b) != 4 {
		return errors.New("expected 4 bytes")
	}
	*i = Integer(binary.BigEndian.Uint32(b))
	return nil
}

func (i *Integer) Read(r io.Reader) error {
	var b [4]byte
	n, err := r.Read(b[:])
	if n == 4 {
		return i.UnmarshalBinary(b[:])
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

// It's perfectly fine to cast these to an int. TODO: Or is it?
func (i Integer) Int() int {
	return int(i)
}

func (i Integer) Uint64() uint64 {
	return uint64(i)
}

func (i Integer) Uint32() uint32 {
	return uint32(i)
}
