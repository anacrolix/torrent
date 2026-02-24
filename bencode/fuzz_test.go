//go:build go1.18
// +build go1.18

package bencode

import (
	"math/big"
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
