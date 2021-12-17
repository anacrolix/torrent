package request_strategy

import (
	"testing"

	"github.com/bradfitz/iter"
)

func benchmarkPieceRequestOrder(
	b *testing.B,
	hintForPiece func(index int) *PieceRequestOrderPathHint,
	numPieces int,
) {
	b.ResetTimer()
	b.ReportAllocs()
	for range iter.N(b.N) {
		pro := NewPieceOrder()
		state := PieceRequestOrderState{}
		doPieces := func(m func(PieceRequestOrderKey)) {
			for i := range iter.N(numPieces) {
				key := PieceRequestOrderKey{
					Index: i,
				}
				pro.PathHint = hintForPiece(i)
				m(key)
			}
		}
		doPieces(func(key PieceRequestOrderKey) {
			pro.Add(key, state)
		})
		state.Availability++
		doPieces(func(key PieceRequestOrderKey) {
			pro.Update(key, state)
		})
		doPieces(func(key PieceRequestOrderKey) {
			state.Priority = piecePriority(key.Index / 4)
			pro.Update(key, state)
		})
		// state.Priority = 0
		state.Availability++
		doPieces(func(key PieceRequestOrderKey) {
			pro.Update(key, state)
		})
		state.Availability--
		doPieces(func(key PieceRequestOrderKey) {
			pro.Update(key, state)
		})
		doPieces(pro.Delete)
		if pro.Len() != 0 {
			b.FailNow()
		}
	}
}

func BenchmarkPieceRequestOrder(b *testing.B) {
	const numPieces = 2000
	b.Run("NoPathHints", func(b *testing.B) {
		benchmarkPieceRequestOrder(b, func(int) *PieceRequestOrderPathHint {
			return nil
		}, numPieces)
	})
	b.Run("SharedPathHint", func(b *testing.B) {
		var pathHint PieceRequestOrderPathHint
		benchmarkPieceRequestOrder(b, func(int) *PieceRequestOrderPathHint {
			return &pathHint
		}, numPieces)
	})
	b.Run("PathHintPerPiece", func(b *testing.B) {
		pathHints := make([]PieceRequestOrderPathHint, numPieces)
		benchmarkPieceRequestOrder(b, func(index int) *PieceRequestOrderPathHint {
			return &pathHints[index]
		}, numPieces)
	})
}
