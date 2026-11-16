package peer_protocol

import (
	"bufio"
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

// readBufferedInteger reads a big-endian Integer straight out of br's buffer with Peek and Discard,
// so there's no scratch buffer to escape to the heap the way Integer.Read's does. At most limit
// bytes are consumed, as though the input ended there. End of input is handled the way
// io.ReadFull and a byte-at-a-time read handle it: the bytes that were available are consumed,
// and the error is io.EOF if there were none, io.ErrUnexpectedEOF if the value was truncated.
// Other errors from the underlying reader are passed through. It returns the bytes consumed.
func readBufferedInteger(br *bufio.Reader, limit int64, i *Integer) (n int, err error) {
	want := 4
	if limit < 4 {
		want = int(max(limit, 0))
	}
	b, err := br.Peek(want)
	if err == nil && want == 4 {
		*i = Integer(binary.BigEndian.Uint32(b))
	}
	// Can't fail: the bytes are already buffered.
	n, _ = br.Discard(len(b))
	if err == nil && want < 4 {
		err = io.EOF
	}
	if err == io.EOF && n != 0 {
		err = io.ErrUnexpectedEOF
	}
	return
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
