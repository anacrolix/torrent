package bencode

import (
	"strings"
	"testing"

	qt "github.com/go-quicktest/qt"
)

// The raw value given to an Unmarshaler used to be left in the decoder buffer, and then prefixed
// the next integer read by the same Decoder.
func TestDecoderIntAfterUnmarshaler(t *testing.T) {
	d := NewDecoder(strings.NewReader("3:abci5e"))
	var b Bytes
	qt.Assert(t, qt.IsNil(d.Decode(&b)))
	qt.Check(t, qt.DeepEquals(b, Bytes("3:abc")))
	var i int
	qt.Assert(t, qt.IsNil(d.Decode(&i)))
	qt.Check(t, qt.Equals(i, 5))
}

// An integer that failed to decode with a type error used to be left in the decoder buffer.
func TestDecoderIntAfterIntTypeError(t *testing.T) {
	d := NewDecoder(strings.NewReader("i1ei2e"))
	var s string
	qt.Assert(t, qt.IsNotNil(d.Decode(&s)))
	var i int
	qt.Assert(t, qt.IsNil(d.Decode(&i)))
	qt.Check(t, qt.Equals(i, 2))
}

// A dict key longer than bytes.MinRead, read from a stream, used to be left in the decoder buffer.
func TestDecoderLongStructKey(t *testing.T) {
	key := strings.Repeat("k", 600)
	d := NewDecoder(strings.NewReader("d600:" + key + "3:abc1:xi5ee"))
	var v struct {
		X int `bencode:"x"`
	}
	qt.Assert(t, qt.IsNil(d.Decode(&v)))
	qt.Check(t, qt.Equals(v.X, 5))
}
