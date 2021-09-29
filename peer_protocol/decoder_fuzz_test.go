package peer_protocol

import (
	"bufio"
	"bytes"
	"testing"
)

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x00"))
	f.Add([]byte("\x00\x00\x00\x01\x00"))
	f.Add([]byte("\x00\x00\x00\x03\x14\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		d := Decoder{
			R: bufio.NewReader(bytes.NewReader(b)),
		}
		var m Message
		err := d.Decode(&m)
		if err != nil {
			t.Skip(err)
		}
	})
}
