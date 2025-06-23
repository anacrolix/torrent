//go:build go1.18
// +build go1.18

package peer_protocol

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	qt "github.com/go-quicktest/qt"
)

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x00"))
	f.Add([]byte("\x00\x00\x00\x03\x14\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x07"))
	f.Fuzz(func(t *testing.T, b []byte) {
		t.Logf("%q", b)
		d := Decoder{
			R:         bufio.NewReader(bytes.NewReader(b)),
			MaxLength: 0x100,
		}
		var ms []Message
		for {
			var m Message
			err := d.Decode(&m)
			t.Log(err)
			if err == io.EOF {
				break
			}
			if err == nil {
				qt.Assert(t, qt.Not(qt.Equals(m, Message{})))
				ms = append(ms, m)
				continue
			} else {
				t.Skip(err)
			}
		}
		t.Log(ms)
		var buf bytes.Buffer
		for _, m := range ms {
			buf.Write(m.MustMarshalBinary())
		}
		if len(b) == 0 {
			qt.Assert(t, qt.HasLen(buf.Bytes(), 0))
		} else {
			qt.Assert(t, qt.DeepEquals(buf.Bytes(), b))
		}
	})
}

func FuzzMessageMarshalBinary(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		var m Message
		if err := m.UnmarshalBinary(b); err != nil {
			t.Skip(err)
		}
		b0 := m.MustMarshalBinary()
		qt.Assert(t, qt.DeepEquals(b0, b))
	})
}
