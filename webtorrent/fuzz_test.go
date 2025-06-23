//go:build go1.18
// +build go1.18

package webtorrent

import (
	"encoding/json"
	"testing"

	qt "github.com/go-quicktest/qt"
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
		out, err := decodeJsonByteString(jsonStr, []byte{})
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.DeepEquals(out, in))
	})
}
