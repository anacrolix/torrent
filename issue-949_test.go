package torrent

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/torrent/metainfo"
)

func TestIssue949LastPieceZeroPadding(t *testing.T) {
	// This torrent has a padding file after the last file listed in the v2 info file tree.
	mi, err := metainfo.LoadFromFile("testdata/issue-949.torrent")
	if err != nil {
		panic(err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		panic(err)
	}
	lastPiece := info.Piece(info.NumPieces() - 1)
	c := qt.New(t)
	c.Assert(info.FilesArePieceAligned(), qt.IsTrue)
	// Check the v1 piece length includes the trailing padding file.
	c.Check(lastPiece.V1Length(), qt.Equals, info.PieceLength)
	// The v2 piece should only include the file data, which fits inside the piece length for this
	// file.
	c.Check(lastPiece.Length(), qt.Equals, int64(3677645))
}
