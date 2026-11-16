package peer_protocol

import (
	"encoding/binary"
	"io"
	"math"

	"github.com/pkg/errors"
)

type (
	// An alias for the underlying type of Integer. This is needed for fuzzing.
	IntegerKind = uint32
	Integer     IntegerKind
)

const IntegerMax = math.MaxUint32

func (i *Integer) UnmarshalBinary(b []byte) error {
	if len(b) != 4 {
		return errors.New("expected 4 bytes")
	}
	*i = Integer(binary.BigEndian.Uint32(b))
	return nil
}

func (i *Integer) Read(r io.Reader) error {
	var b [4]byte
	n, err := io.ReadFull(r, b[:])
	if err == nil {
		if n != 4 {
			panic(n)
		}
		return i.UnmarshalBinary(b[:])
	}
	return err
}

// readInteger reads a big-endian Integer from a byte reader. It avoids the heap allocation that
// Integer.Read incurs when its buffer escapes through the io.Reader interface, and matches
// io.ReadFull's end-of-input behaviour: io.EOF if nothing was read, io.ErrUnexpectedEOF if the
// value was truncated.
func readInteger(r io.ByteReader, i *Integer) error {
	var b [4]byte
	for j := range b {
		c, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && j != 0 {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		b[j] = c
	}
	*i = Integer(binary.BigEndian.Uint32(b[:]))
	return nil
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
