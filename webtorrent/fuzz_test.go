//go:build go1.18
// +build go1.18

package webtorrent

import (
	"encoding/json"
	"testing"

	qt "github.com/frankban/quicktest"
)

func FuzzJsonBinaryStrings(f *testing.F) {
	f.Fuzz(func(t *testing.T, in []byte) {
		jsonBytes, err := json.Marshal(binaryToJsonString(in))
		if err != nil {
			t.Fatal(err)
		}
		// t.Logf("%q", jsonBytes)
		var jsonStr string
		err = json.Unmarshal(jsonBytes, &jsonStr)
		if err != nil {
			t.Fatal(err)
		}
		// t.Logf("%q", jsonStr)
		c := qt.New(t)
		out, err := decodeJsonByteString(jsonStr, []byte{})
		c.Assert(err, qt.IsNil)
		c.Assert(out, qt.DeepEquals, in)
	})
}
