package bencode

import (
	"testing"

	qt "github.com/frankban/quicktest"
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
	c := qt.New(t)
	c.Assert(err, qt.IsNotNil)
}

type structWithOmitEmptyBytes struct {
	A Bytes `bencode:",omitempty"`
	B Bytes `bencode:",omitempty"`
}

func TestMarshalNilStructBytesOmitEmpty(t *testing.T) {
	c := qt.New(t)
	b, err := Marshal(structWithOmitEmptyBytes{B: Bytes("i42e")})
	c.Assert(err, qt.IsNil)
	t.Logf("%q", b)
	var s structWithBytes
	err = Unmarshal(b, &s)
	c.Assert(err, qt.IsNil)
	c.Check(s.B, qt.DeepEquals, Bytes("i42e"))
}
