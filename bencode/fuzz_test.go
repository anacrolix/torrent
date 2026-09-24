//go:build go1.18
// +build go1.18

package bencode

import (
	"math/big"
	"strings"
	"testing"

	qt "github.com/go-quicktest/qt"
	"github.com/google/go-cmp/cmp"
)

func Fuzz(f *testing.F) {
	for _, ret := range random_encode_tests {
		f.Add([]byte(ret.expected))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var d interface{}
		err := Unmarshal(b, &d)
		if err != nil {
			t.Skip()
		}
		b0, err := Marshal(d)
		qt.Assert(t, qt.IsNil(err))
		var d0 interface{}
		err = Unmarshal(b0, &d0)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.CmpEquals(d0, d, cmp.Comparer(func(a, b *big.Int) bool {
			return a.Cmp(b) == 0
		})))
	})
}

func FuzzInterfaceRoundTrip(f *testing.F) {
	for _, ret := range random_encode_tests {
		f.Add([]byte(ret.expected))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var d interface{}
		err := Unmarshal(b, &d)
		if err != nil {
			t.Skip(err)
		}
		b0, err := Marshal(d)
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.DeepEquals(b0, b))
	})
}

// depthOf returns the maximum nesting depth of a decoded value, where an empty dict or list has
// depth 1.
func depthOf(v interface{}) (depth int) {
	switch x := v.(type) {
	case map[string]interface{}:
		depth = 1
		for _, e := range x {
			if d := depthOf(e) + 1; d > depth {
				depth = d
			}
		}
	case []interface{}:
		depth = 1
		for _, e := range x {
			if d := depthOf(e) + 1; d > depth {
				depth = d
			}
		}
	}
	return
}

// FuzzDecodeDepthLimited ensures decoding never produces values nested deeper than the default
// limit: deeper input must be rejected with an error (never crash with unbounded recursion), and
// any value that does decode must round-trip through Marshal without tripping the encoder.
func FuzzDecodeDepthLimited(f *testing.F) {
	for depth := 0; depth <= DefaultMaxDepth+1; depth++ {
		f.Add([]byte(strings.Repeat("l", depth) + "i0e" + strings.Repeat("e", depth)))
		f.Add([]byte(strings.Repeat("d1:a", depth) + "i0e" + strings.Repeat("e", depth)))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var d interface{}
		err := Unmarshal(b, &d)
		if err != nil {
			t.Skip(err)
		}
		if got := depthOf(d); got > DefaultMaxDepth {
			t.Fatalf("decoded value has nesting depth %d, exceeding limit %d", got, DefaultMaxDepth)
		}
		// The depth-limited value must round-trip through Marshal and decode again within the
		// limit.
		b0, err := Marshal(d)
		qt.Assert(t, qt.IsNil(err))
		var d0 interface{}
		qt.Assert(t, qt.IsNil(Unmarshal(b0, &d0)))
	})
}
