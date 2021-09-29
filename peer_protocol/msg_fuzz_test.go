package peer_protocol

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

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
