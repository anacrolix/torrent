package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"

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
	qt.Assert(t, qt.IsTrue(info.FilesArePieceAligned()))
	// Check the v1 piece length includes the trailing padding file.
	qt.Check(t, qt.Equals(lastPiece.V1Length(), info.PieceLength))
	// The v2 piece should only include the file data, which fits inside the piece length for this
	// file.
	qt.Check(t, qt.Equals(lastPiece.Length(), int64(3677645)))
}
