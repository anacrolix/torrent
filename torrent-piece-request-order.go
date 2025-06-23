package torrent

import (
	g "github.com/anacrolix/generics"

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
