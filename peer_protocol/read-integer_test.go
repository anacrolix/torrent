package peer_protocol

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	qt "github.com/go-quicktest/qt"
)

// byteReaderInteger is the byte-at-a-time integer read that readBufferedInteger replaced. It's the
// reference for the differential fuzz below.
func byteReaderInteger(r io.ByteReader, i *Integer) error {
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

var errInjected = errors.New("injected")

// chunkedReader returns data at most chunk bytes at a time, then fails with err once failAt bytes
// have been returned (or data runs out, with io.EOF). The failure is sticky. If withData, the
// failure is returned together with the last bytes, as io.Reader permits.
type chunkedReader struct {
	data     []byte
	chunk    int
	failAt   int
	err      error
	withData bool
	pos      int
}

func (r *chunkedReader) Read(p []byte) (n int, err error) {
	end := min(len(r.data), r.failAt)
	if r.pos >= end {
		if r.pos >= len(r.data) {
			return 0, io.EOF
		}
		return 0, r.err
	}
	n = copy(p[:min(len(p), r.chunk)], r.data[r.pos:end])
	r.pos += n
	if r.withData && r.pos >= end {
		if r.pos >= len(r.data) {
			err = io.EOF
		} else {
			err = r.err
		}
	}
	return
}

type integerReadResult struct {
	value     Integer
	err       error
	remaining []byte
	unread    int64
}

func FuzzReadBufferedInteger(f *testing.F) {
	for _, data := range []string{"", "\x01", "\x01\x02", "\x01\x02\x03", "\x01\x02\x03\x04", "\x01\x02\x03\x04\x05"} {
		for _, limit := range []int64{-1, 0, 1, 3, 4, 5} {
			for failAt := range 6 {
				for _, errKind := range []uint8{0, 1} {
					f.Add([]byte(data), uint8(1), uint8(failAt), errKind, false, limit, true)
					f.Add([]byte(data), uint8(3), uint8(failAt), errKind, true, limit, false)
				}
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, chunk, failAt, errKind uint8, withData bool, limit int64, header bool) {
		run := func(read func(br *bufio.Reader, dr *decodeReader, i *Integer) error) (res integerReadResult) {
			src := &chunkedReader{
				data:     data,
				chunk:    max(int(chunk), 1),
				failAt:   int(failAt),
				err:      errInjected,
				withData: withData,
			}
			if errKind%2 == 1 {
				src.err = io.EOF
			}
			// Minimum size, so refills happen often.
			br := bufio.NewReaderSize(src, 16)
			var dr decodeReader
			dr.Init(br, limit)
			res.err = read(br, &dr, &res.value)
			res.unread = dr.lr.N
			res.remaining, _ = br.Peek(br.Buffered())
			res.remaining = append([]byte(nil), res.remaining...)
			return
		}
		var old, new integerReadResult
		if header {
			old = run(func(br *bufio.Reader, _ *decodeReader, i *Integer) error {
				return byteReaderInteger(br, i)
			})
			new = run(func(br *bufio.Reader, _ *decodeReader, i *Integer) error {
				_, err := readBufferedInteger(br, 4, i)
				return err
			})
		} else {
			old = run(func(_ *bufio.Reader, dr *decodeReader, i *Integer) error {
				return byteReaderInteger(expectReader{dr}, i)
			})
			new = run(func(_ *bufio.Reader, dr *decodeReader, i *Integer) error {
				return expectReader{dr}.readInteger(i)
			})
		}
		qt.Assert(t, qt.Equals(new.err, old.err))
		qt.Assert(t, qt.Equals(new.value, old.value))
		qt.Assert(t, qt.Equals(new.unread, old.unread))
		qt.Assert(t, qt.DeepEquals(new.remaining, old.remaining))
	})
}

func TestReadBufferedIntegerEOF(t *testing.T) {
	read := func(s string) (Integer, error) {
		var i Integer
		_, err := readBufferedInteger(bufio.NewReader(&chunkedReader{data: []byte(s), chunk: 1, failAt: len(s)}), 4, &i)
		return i, err
	}
	_, err := read("")
	qt.Assert(t, qt.Equals(err, io.EOF))
	for _, s := range []string{"\x01", "\x01\x02", "\x01\x02\x03"} {
		_, err = read(s)
		qt.Assert(t, qt.Equals(err, io.ErrUnexpectedEOF))
	}
	i, err := read("\x01\x02\x03\x04")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(i, 0x01020304))
}
