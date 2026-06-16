package metainfo

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestChoosePieceLengthEx(t *testing.T) {
	const (
		KiB = 1 << 10
		MiB = 1 << 20
		GiB = 1 << 30
	)
	for _, c := range []struct {
		name string
		opts ChoosePieceLengthOpts
		want int64
	}{
		{
			name: "defaults double until under soft max",
			opts: ChoosePieceLengthOpts{TotalLength: 1 * GiB},
			want: 1 * MiB,
		},
		{
			name: "defaults small total stays at min",
			opts: ChoosePieceLengthOpts{TotalLength: 100 * 16 * KiB},
			want: 16 * KiB,
		},
		{
			name: "custom min piece size is the floor",
			opts: ChoosePieceLengthOpts{TotalLength: 4 * GiB, MinPieceSize: 1 * MiB},
			want: 4 * MiB,
		},
		{
			name: "max piece size caps growth above soft max",
			opts: ChoosePieceLengthOpts{TotalLength: 1 * GiB, MaxPieceSize: 256 * KiB},
			want: 256 * KiB,
		},
		{
			name: "wide soft band lands inside it",
			opts: ChoosePieceLengthOpts{TotalLength: 1 * GiB, SoftMinPieceCount: 1000, SoftMaxPieceCount: 8000},
			want: 256 * KiB,
		},
		{
			name: "soft min floor blocks the last halving",
			opts: ChoosePieceLengthOpts{TotalLength: 2500 * 16 * KiB, SoftMinPieceCount: 1500, SoftMaxPieceCount: 2000},
			want: 16 * KiB,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			qt.Check(t, qt.Equals(ChoosePieceLengthEx(c.opts), c.want))
		})
	}
}

// Erigon's downloadercfg.DefaultPieceSize.
const erigonDefaultPieceSize = 2 << 20

func TestChoosePieceLengthErigon(t *testing.T) {
	const (
		MiB = 1 << 20
		GiB = 1 << 30
	)
	for _, c := range []struct {
		totalLength     int64
		wantPieceLength int64
		wantPieces      int64
	}{
		{totalLength: 100, wantPieceLength: 2 * MiB, wantPieces: 1},
		{totalLength: 10 * MiB, wantPieceLength: 2 * MiB, wantPieces: 5},
		{totalLength: 1 * GiB, wantPieceLength: 2 * MiB, wantPieces: 512},
		{totalLength: 10 * GiB, wantPieceLength: 8 * MiB, wantPieces: 1280},
		{totalLength: 100 * GiB, wantPieceLength: 64 * MiB, wantPieces: 1600},
		{totalLength: 137613017000, wantPieceLength: 64 * MiB, wantPieces: 2051},
	} {
		t.Run("", func(t *testing.T) {
			pieceLength := ChoosePieceLengthEx(ChoosePieceLengthOpts{
				TotalLength:  c.totalLength,
				MinPieceSize: erigonDefaultPieceSize,
				MaxPieceSize: 64 * MiB,
			})
			qt.Check(t, qt.Equals(pieceLength, c.wantPieceLength))
			pieces := (c.totalLength + pieceLength - 1) / pieceLength
			qt.Check(t, qt.Equals(pieces, c.wantPieces))
		})
	}
}

func TestChoosePieceLengthMatchesEx(t *testing.T) {
	for _, totalLength := range []int64{0, 1, 1 << 10, 1 << 20, 1 << 30, 123456789} {
		qt.Check(t, qt.Equals(
			ChoosePieceLength(totalLength),
			ChoosePieceLengthEx(ChoosePieceLengthOpts{TotalLength: totalLength}),
		))
	}
}
