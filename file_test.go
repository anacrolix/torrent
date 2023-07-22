package torrent

import (
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/stretchr/testify/assert"
)

func TestFileExclusivePieces(t *testing.T) {
	for _, _case := range []struct {
		off, size, pieceSize int64
		begin, end           int
	}{
		{0, 2, 2, 0, 1},
		{1, 2, 2, 1, 1},
		{1, 4, 2, 1, 2},
	} {
		begin, end := byteRegionExclusivePieces(_case.off, _case.size, _case.pieceSize)
		assert.EqualValues(t, _case.begin, begin)
		assert.EqualValues(t, _case.end, end)
	}
}

type testFileBytesLeft struct {
	usualPieceSize  int64
	firstPieceIndex int
	endPieceIndex   int
	fileOffset      int64
	fileLength      int64
	completedPieces roaring.Bitmap
	expected        int64
	name            string
}

func (me testFileBytesLeft) Run(t *testing.T) {
	t.Run(me.name, func(t *testing.T) {
		assert.EqualValues(t, me.expected, fileBytesLeft(me.usualPieceSize, me.firstPieceIndex, me.endPieceIndex, me.fileOffset, me.fileLength, &me.completedPieces, func(pieceIndex int) int64 {
			return 0
		}))
	})
}

func TestFileBytesLeft(t *testing.T) {
	testFileBytesLeft{
		usualPieceSize:  3,
		firstPieceIndex: 1,
		endPieceIndex:   1,
		fileOffset:      1,
		fileLength:      0,
		expected:        0,
		name:            "ZeroLengthFile",
	}.Run(t)

	testFileBytesLeft{
		usualPieceSize:  2,
		firstPieceIndex: 1,
		endPieceIndex:   2,
		fileOffset:      1,
		fileLength:      1,
		expected:        1,
		name:            "EndOfSecondPiece",
	}.Run(t)

	testFileBytesLeft{
		usualPieceSize:  3,
		firstPieceIndex: 0,
		endPieceIndex:   1,
		fileOffset:      1,
		fileLength:      1,
		expected:        1,
		name:            "FileInFirstPiece",
	}.Run(t)

	testFileBytesLeft{
		usualPieceSize:  3,
		firstPieceIndex: 0,
		endPieceIndex:   1,
		fileOffset:      1,
		fileLength:      1,
		expected:        1,
		name:            "LandLocked",
	}.Run(t)

	testFileBytesLeft{
		usualPieceSize:  3,
		firstPieceIndex: 1,
		endPieceIndex:   3,
		fileOffset:      4,
		fileLength:      4,
		expected:        4,
		name:            "TwoPieces",
	}.Run(t)

	testFileBytesLeft{
		usualPieceSize:  3,
		firstPieceIndex: 1,
		endPieceIndex:   4,
		fileOffset:      5,
		fileLength:      7,
		expected:        7,
		name:            "ThreePieces",
	}.Run(t)

	testFileBytesLeft{
		usualPieceSize:  3,
		firstPieceIndex: 1,
		endPieceIndex:   4,
		fileOffset:      5,
		fileLength:      7,
		expected:        0,
		completedPieces: func() (ret roaring.Bitmap) {
			ret.AddRange(0, 5)
			return
		}(),
		name: "ThreePiecesCompletedAll",
	}.Run(t)
}
