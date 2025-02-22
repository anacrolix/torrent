//go:build go1.18
// +build go1.18

package krpc

import (
	"bytes"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	qt "github.com/frankban/quicktest"
)

// Check that if we can unmarshal a Msg that we can marshal it back without an error. We
// intentionally don't require that it marshals back to the same bytes as fields unknown to Msg may
// have been dropped. But if it does, then I think that makes it an "interesting" case.
func Fuzz(f *testing.F) {
	f.Add([]byte("d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01e1:t1:t1:y1:re"))
	f.Fuzz(func(t *testing.T, in []byte) {
		c := qt.New(t)
		var m Msg
		err := bencode.Unmarshal(in, &m)
		if err != nil {
			t.Skip()
		}
		out, err := bencode.Marshal(m)
		c.Assert(err, qt.IsNil)
		if !bytes.Equal(in, out) {
			t.Skip()
		}
	})
}
