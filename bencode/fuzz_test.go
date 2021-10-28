//go:build go1.18
// +build go1.18

package bencode

import (
	"math/big"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp"
)

var bencodeInterfaceChecker = qt.CmpEquals(cmp.Comparer(func(a, b *big.Int) bool {
	return a.Cmp(b) == 0
}))

func Fuzz(f *testing.F) {
	for _, ret := range random_encode_tests {
		f.Add([]byte(ret.expected))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		c := qt.New(t)
		var d interface{}
		err := Unmarshal(b, &d)
		if err != nil {
			t.Skip()
		}
		b0, err := Marshal(d)
		c.Assert(err, qt.IsNil)
		var d0 interface{}
		err = Unmarshal(b0, &d0)
		c.Assert(err, qt.IsNil)
		c.Assert(d0, bencodeInterfaceChecker, d)
	})
}
