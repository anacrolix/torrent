package requestStrategy

import (
	"slices"
	"testing"
	"unique"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types"
)

type testTorrent struct {
	checked []int
}

func (t *testTorrent) PieceRequest(i int) bool {
	t.checked = append(t.checked, i)
	return true
}

func (t *testTorrent) PieceCountUnverified(int) bool { return false }

func (t *testTorrent) PieceLength() int64 { return 1 << 14 }

type testInput struct {
	t *testTorrent
}

func (in testInput) Torrent(metainfo.Hash) Torrent { return in.t }

func (testInput) Capacity() (int64, bool) { return 0, false }

func (testInput) MaxUnverifiedBytes() int64 { return 0 }

// Pieces nobody wants sort after every wanted piece, so the scan stops at the first of them
// instead of checking every incomplete piece of the torrent.
func TestGetRequestablePiecesStopsAtUnwantedPieces(t *testing.T) {
	ih := unique.Make(metainfo.Hash{})
	pro := NewPieceOrder(NewAjwernerBtree(), 0)
	for i := range 1000 {
		priority := types.PiecePriorityNone
		if i >= 500 && i < 503 {
			priority = types.PiecePriorityNormal
		}
		pro.Add(PieceRequestOrderKey{InfoHash: ih, Index: i}, PieceRequestOrderState{Priority: priority, Availability: 1})
	}
	tor := &testTorrent{}
	var requested []int
	GetRequestablePieces(testInput{tor}, pro, func(_ metainfo.Hash, index int, _ PieceRequestOrderState) bool {
		requested = append(requested, index)
		return true
	})
	want := []int{500, 501, 502}
	if !slices.Equal(requested, want) {
		t.Errorf("requested %v, want %v", requested, want)
	}
	if !slices.Equal(tor.checked, want) {
		t.Errorf("checked %v, want only the wanted pieces %v", tor.checked, want)
	}
}

// A streamed torrent: tens of thousands of incomplete pieces, with only a reader's window wanted.
func BenchmarkGetRequestablePiecesStreaming(b *testing.B) {
	ih := unique.Make(metainfo.Hash{})
	pro := NewPieceOrder(NewAjwernerBtree(), 0)
	for i := range 20000 {
		priority := types.PiecePriorityNone
		if i >= 10000 && i < 10016 {
			priority = types.PiecePriorityReadahead
		}
		pro.Add(PieceRequestOrderKey{InfoHash: ih, Index: i}, PieceRequestOrderState{Priority: priority, Availability: 1})
	}
	input := testInput{&testTorrent{}}
	b.ReportAllocs()
	for b.Loop() {
		input.t.checked = input.t.checked[:0]
		GetRequestablePieces(input, pro, func(metainfo.Hash, int, PieceRequestOrderState) bool {
			return true
		})
	}
}
