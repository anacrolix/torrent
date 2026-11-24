package torrent

import (
	"math/rand"
	"testing"

	"github.com/RoaringBitmap/roaring/v2"
	qt "github.com/go-quicktest/qt"
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
		qt.Check(t, qt.Equals(begin, _case.begin))
		qt.Check(t, qt.Equals(end, _case.end))
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
		qt.Check(t, qt.Equals(fileBytesLeft(me.usualPieceSize, me.firstPieceIndex, me.endPieceIndex, me.fileOffset, me.fileLength, &me.completedPieces, func(pieceIndex int) int64 {
			return 0
		}), me.expected))
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

// makePieces carries a file cursor forward across pieces instead of rescanning from the first file.
// This checks the cursored pieceFirstFileIndex returns exactly what the original always-from-zero
// scan did, for every piece, across many file layouts including empty files and a zero piece length.
func TestMakePiecesBeginFileEquivalence(t *testing.T) {
	// The original scan, always from the first file.
	origFrom0 := func(pieceOffset int64, files []*File) int {
		for i, f := range files {
			if f.offset+f.length > pieceOffset {
				return i
			}
		}
		return 0
	}
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 5000; trial++ {
		// Build a contiguous file layout with a random mix of lengths, heavy on empty files.
		n := rng.Intn(14) + 1
		var files []*File
		var offset int64
		for i := 0; i < n; i++ {
			var length int64
			switch rng.Intn(3) {
			case 0:
				length = 0 // empty file
			default:
				length = int64(rng.Intn(9))
			}
			files = append(files, &File{offset: offset, length: length})
			offset += length
		}
		total := offset
		// Piece length 0 (degenerate, all piece offsets 0) and a few positive ones.
		for _, pieceLength := range []int64{0, 1, 3, 4} {
			var numPieces int
			if pieceLength == 0 {
				numPieces = rng.Intn(4) // matches NumPieces using len(Pieces)/20 for a 0-length torrent
			} else {
				numPieces = int((total + pieceLength - 1) / pieceLength)
			}
			// The cursored sweep, exactly as makePieces runs it.
			cursor := 0
			for i := 0; i < numPieces; i++ {
				pieceOffset := int64(i) * pieceLength
				cursor = pieceFirstFileIndex(pieceOffset, files, cursor)
				want := origFrom0(pieceOffset, files)
				if cursor != want {
					t.Fatalf("trial %d pieceLen %d piece %d (offset %d): cursor=%d orig=%d files=%v",
						trial, pieceLength, i, pieceOffset, cursor, want, files)
				}
			}
		}
	}
}
