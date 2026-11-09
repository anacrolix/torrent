package torrent

import (
	"testing"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

// Info from peers carries no piece layers, so the pieces of a v2 file longer than a piece have no
// hash. They're pending, to get their hashes, but can't be requested, which must not trip the
// pending pieces check.
func TestV2InfoWithoutPieceLayersPendingPieces(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	info, err := mi.UnmarshalInfo()
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsFalse(info.HasV1()))

	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHashV2: g.Some(infohash_v2.HashBytes(mi.InfoBytes)),
	})
	qt.Assert(t, qt.IsNil(tt.SetInfoBytes(mi.InfoBytes)))

	cl.lock()
	defer cl.unlock()
	qt.Assert(t, qt.IsFalse(tt._pendingPieces.IsEmpty()))
	tt.assertPendingPiecesMatchRequestOrder()
}
