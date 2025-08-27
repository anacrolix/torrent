package requestStrategy

import (
	"testing"
	"unique"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/bradfitz/iter"
)

func benchmarkPieceRequestOrder[B Btree](
	b *testing.B,
	// Initialize the next run, and return a Btree
	newBtree func() B,
	// Set any path hinting for the specified piece
	hintForPiece func(index int),
	numPieces int,
) {
	b.ReportAllocs()
	zeroHashHandle := unique.Make(metainfo.Hash{})
	for b.Loop() {
		pro := NewPieceOrder(newBtree(), numPieces)
		state := PieceRequestOrderState{}
		doPieces := func(m func(PieceRequestOrderKey) bool) {
			for i := range iter.N(numPieces) {
				key := PieceRequestOrderKey{
					Index:    i,
					InfoHash: zeroHashHandle,
				}
				hintForPiece(i)
				m(key)
			}
		}
		doPieces(func(key PieceRequestOrderKey) bool {
			return !pro.Add(key, state).Ok
		})
		state.Availability++
		doPieces(func(key PieceRequestOrderKey) bool {
			pro.Update(key, state)
			return true
		})
		pro.tree.Scan(func(item PieceRequestOrderItem) bool {
			return true
		})
		doPieces(func(key PieceRequestOrderKey) bool {
			state.Priority = piecePriority(key.Index / 4)
			pro.Update(key, state)
			return true
		})
		pro.tree.Scan(func(item PieceRequestOrderItem) bool {
			return item.Key.Index < 1000
		})
		state.Priority = 0
		state.Availability++
		doPieces(func(key PieceRequestOrderKey) bool {
			pro.Update(key, state)
			return true
		})
		pro.tree.Scan(func(item PieceRequestOrderItem) bool {
			return item.Key.Index < 1000
		})
		state.Availability--
		doPieces(func(key PieceRequestOrderKey) bool {
			pro.Update(key, state)
			return true
		})
		doPieces(pro.Delete)
		if pro.Len() != 0 {
			b.FailNow()
		}
	}
}

func zero[T any](t *T) {
	var zt T
	*t = zt
}

func BenchmarkPieceRequestOrder(b *testing.B) {
	const numPieces = 2000
	b.Run("TidwallBtree", func(b *testing.B) {
		b.Run("NoPathHints", func(b *testing.B) {
			benchmarkPieceRequestOrder(b, NewTidwallBtree, func(int) {}, numPieces)
		})
		b.Run("SharedPathHint", func(b *testing.B) {
			var pathHint PieceRequestOrderPathHint
			var btree *tidwallBtree
			benchmarkPieceRequestOrder(
				b, func() *tidwallBtree {
					zero(&pathHint)
					btree = NewTidwallBtree()
					btree.PathHint = &pathHint
					return btree
				}, func(int) {}, numPieces,
			)
		})
		b.Run("PathHintPerPiece", func(b *testing.B) {
			pathHints := make([]PieceRequestOrderPathHint, numPieces)
			var btree *tidwallBtree
			benchmarkPieceRequestOrder(
				b, func() *tidwallBtree {
					btree = NewTidwallBtree()
					return btree
				}, func(index int) {
					btree.PathHint = &pathHints[index]
				}, numPieces,
			)
		})
	})
	b.Run("AjwernerBtree", func(b *testing.B) {
		benchmarkPieceRequestOrder(b, NewAjwernerBtree, func(index int) {}, numPieces)
	})
}
