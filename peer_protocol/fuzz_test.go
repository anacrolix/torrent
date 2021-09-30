//go:build go1.18

package peer_protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"

	qt "github.com/frankban/quicktest"
)

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x00"))
	f.Add([]byte("\x00\x00\x00\x03\x14\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
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
			if errors.Is(err, io.EOF) {
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
		var buf bytes.Buffer
		for _, m := range ms {
			buf.Write(m.MustMarshalBinary())
		}
		c.Assert(buf.Bytes(), qt.DeepEquals, b)
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
