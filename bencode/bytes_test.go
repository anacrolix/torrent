package bencode

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestBytesMarshalNil(t *testing.T) {
	var b Bytes
	Marshal(b)
}

type structWithBytes struct {
	A Bytes
	B Bytes
}

func TestMarshalNilStructBytes(t *testing.T) {
	_, err := Marshal(structWithBytes{B: Bytes("i42e")})
	qt.Assert(t, qt.IsNotNil(err))
}

type structWithOmitEmptyBytes struct {
	A Bytes `bencode:",omitempty"`
	B Bytes `bencode:",omitempty"`
}

func TestMarshalNilStructBytesOmitEmpty(t *testing.T) {
	b, err := Marshal(structWithOmitEmptyBytes{B: Bytes("i42e")})
	qt.Assert(t, qt.IsNil(err))
	t.Logf("%q", b)
	var s structWithBytes
	err = Unmarshal(b, &s)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(s.B, Bytes("i42e")))
}
