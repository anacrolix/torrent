package torrent

import (
	"fmt"

	"github.com/RoaringBitmap/roaring"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/internal/amortize"
	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
)

// It's probably possible to track whether the piece moves around in the btree to be more efficient
// about triggering request updates.
func (t *Torrent) updatePieceRequestOrderPiece(pieceIndex int) (changed bool) {
	if t.storage == nil {
		return false
	}
	pro, ok := t.cl.pieceRequestOrder[t.clientPieceRequestOrderKey()]
	if !ok {
		return false
	}
	key := t.pieceRequestOrderKey(pieceIndex)
	if t.hasStorageCap() {
		return pro.pieces.Update(key, t.requestStrategyPieceOrderState(pieceIndex))
	}
	// TODO: This might eject a piece that could count toward being unverified?
	pending := !t.ignorePieceForRequests(pieceIndex)
	if pending {
		newState := t.requestStrategyPieceOrderState(pieceIndex)
		old := pro.pieces.Add(key, newState)
		return old.Ok && old.Value != newState
	} else {
		return pro.pieces.Delete(key)
	}
}

func (t *Torrent) clientPieceRequestOrderKey() clientPieceRequestOrderKeySumType {
	if t.storage.Capacity == nil {
		return clientPieceRequestOrderRegularTorrentKey{t}
	}
	return clientPieceRequestOrderSharedStorageTorrentKey{t.storage.Capacity}
}

func (t *Torrent) deletePieceRequestOrder() {
	if t.storage == nil {
		return
	}
	cpro := t.cl.pieceRequestOrder
	key := t.clientPieceRequestOrderKey()
	pro := cpro[key]
	for i := range t.numPieces() {
		pro.pieces.Delete(t.pieceRequestOrderKey(i))
	}
	delete(pro.torrents, t)
	if pro.pieces.Len() == 0 {
		delete(cpro, key)
	}
}

func (t *Torrent) initPieceRequestOrder() {
	if t.storage == nil {
		return
	}
	g.MakeMapIfNil(&t.cl.pieceRequestOrder)
	key := t.clientPieceRequestOrderKey()
	cpro := t.cl.pieceRequestOrder
	if _, ok := cpro[key]; !ok {
		value := clientPieceRequestOrderValue{
			pieces: requestStrategy.NewPieceOrder(requestStrategy.NewAjwernerBtree(), t.numPieces()),
		}
		g.MakeMap(&value.torrents)
		cpro[key] = value
	}
	g.MapMustAssignNew(cpro[key].torrents, t, struct{}{})
}

func (t *Torrent) addRequestOrderPiece(i int) {
	if t.storage == nil {
		return
	}
	pro := t.getPieceRequestOrder()
	key := t.pieceRequestOrderKey(i)
	if t.hasStorageCap() || !t.ignorePieceForRequests(i) {
		pro.Add(key, t.requestStrategyPieceOrderState(i))
	}
}

func (t *Torrent) getPieceRequestOrder() *requestStrategy.PieceRequestOrder {
	if t.storage == nil {
		return nil
	}
	return t.cl.pieceRequestOrder[t.clientPieceRequestOrderKey()].pieces
}

func (t *Torrent) checkPendingPiecesMatchesRequestOrder() {
	if !amortize.Try() {
		return
	}
	short := *t.canonicalShortInfohash()
	var proBitmap roaring.Bitmap
	for item := range t.getPieceRequestOrder().Iter {
		if item.Key.InfoHash.Value() != short {
			continue
		}
		if item.State.Priority == PiecePriorityNone {
			continue
		}
		if t.ignorePieceForRequests(item.Key.Index) {
			continue
		}
		proBitmap.Add(uint32(item.Key.Index))
	}
	if !proBitmap.Equals(&t._pendingPieces) {
		intersection := roaring.And(&proBitmap, &t._pendingPieces)
		exclPro := roaring.AndNot(&proBitmap, intersection)
		exclPending := roaring.AndNot(&t._pendingPieces, intersection)
		panic(fmt.Sprintf("piece request order has %v and pending pieces has %v", exclPro.String(), exclPending.String()))
	}
}
