package metainfo

import (
	"testing"
)

func TestCachedPieceLengthsMatchTheFileTreeWalk(t *testing.T) {
	for _, name := range []string{
		"../testdata/bittorrent-v2-test.torrent",
		"../testdata/bittorrent-v2-hybrid-test.torrent",
		"../testdata/v2-multi-piece.torrent",
	} {
		mi, err := LoadFromFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		info, err := mi.UnmarshalInfo()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !info.HasV2() {
			t.Fatalf("%s is no v2 torrent", name)
		}
		walked := make([]int64, info.NumPieces())
		for i := range walked {
			walked[i] = info.Piece(i).Length()
		}
		info.CachePieceLengths()
		if info.v2Spans == nil {
			t.Fatalf("%s: nothing cached", name)
		}
		for i, want := range walked {
			if got := info.Piece(i).Length(); got != want {
				t.Errorf("%s piece %d: cached length %d, walked %d", name, i, got, want)
			}
		}
	}
}
