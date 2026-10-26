package metainfo

import (
	"bytes"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
)

// A malformed announce-list shouldn't prevent the rest of the metainfo from loading.
func TestLoadMalformedAnnounceList(t *testing.T) {
	b, err := bencode.Marshal(map[string]any{
		"announce":      "http://tracker.example/announce",
		"announce-list": "http://a.example/announce,http://b.example/announce",
		"info": map[string]any{
			"name": "x", "length": 1, "piece length": 16384, "pieces": "",
		},
	})
	qt.Assert(t, qt.IsNil(err))
	mi, err := Load(bytes.NewReader(b))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(mi.Announce, "http://tracker.example/announce"))
	qt.Check(t, qt.HasLen(mi.AnnounceList, 0))
	// Falls back to the single announce URL, like other clients do.
	qt.Check(t, qt.DeepEquals(mi.UpvertedAnnounceList(), AnnounceList{{"http://tracker.example/announce"}}))
}
