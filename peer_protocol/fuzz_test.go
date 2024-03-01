//go:build go1.18
// +build go1.18

package peer_protocol

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	qt "github.com/frankban/quicktest"
)

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x00"))
	f.Add([]byte("\x00\x00\x00\x03\x14\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x07"))
	f.Fuzz(func(t *testing.T, b []byte) {
		t.Logf("%q", b)
		c := qt.New(t)
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
				c.Assert(m, qt.Not(qt.Equals), Message{})
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
			c.Assert(buf.Bytes(), qt.HasLen, 0)
		} else {
			c.Assert(buf.Bytes(), qt.DeepEquals, b)
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
		qt.Assert(t, b0, qt.DeepEquals, b)
	})
}
