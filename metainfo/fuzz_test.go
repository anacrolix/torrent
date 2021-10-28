//go:build go1.18
// +build go1.18

package metainfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/bencode"
)

func Fuzz(f *testing.F) {
	// Is there an OS-agnostic version of Glob?
	matches, err := filepath.Glob(filepath.FromSlash("testdata/*.torrent"))
	if err != nil {
		f.Fatal(err)
	}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Logf("adding %q", m)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var mi MetaInfo
		err := bencode.Unmarshal(b, &mi)
		if err != nil {
			t.Skip(err)
		}
		_, err = bencode.Marshal(mi)
		if err != nil {
			panic(err)
		}
		info, err := mi.UnmarshalInfo()
		if err != nil {
			t.Skip(err)
		}
		_, err = bencode.Marshal(info)
		if err != nil {
			panic(err)
		}
	})
}
