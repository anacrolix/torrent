package torrent

import (
	g "github.com/anacrolix/generics"

	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/storage"
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
		return pro.Update(key, t.requestStrategyPieceOrderState(pieceIndex))
	}
	pending := !t.ignorePieceForRequests(pieceIndex)
	if pending {
		newState := t.requestStrategyPieceOrderState(pieceIndex)
		old := pro.Add(key, newState)
		return old.Ok && old.Value != newState
	} else {
		return pro.Delete(key)
	}
}

func (t *Torrent) clientPieceRequestOrderKey() clientPieceRequestOrderKeySumType {
	if t.storage.Capacity == nil {
		return clientPieceRequestOrderKey[*Torrent]{t}
	}
	return clientPieceRequestOrderKey[storage.TorrentCapacity]{t.storage.Capacity}
}

func (t *Torrent) deletePieceRequestOrder() {
	if t.storage == nil {
		return
	}
	cpro := t.cl.pieceRequestOrder
	key := t.clientPieceRequestOrderKey()
	pro := cpro[key]
	for i := 0; i < t.numPieces(); i++ {
		pro.Delete(t.pieceRequestOrderKey(i))
	}
	if pro.Len() == 0 {
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
	if cpro[key] == nil {
		cpro[key] = request_strategy.NewPieceOrder(request_strategy.NewAjwernerBtree(), t.numPieces())
	}
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

func (t *Torrent) getPieceRequestOrder() *request_strategy.PieceRequestOrder {
	return t.cl.pieceRequestOrder[t.clientPieceRequestOrderKey()]
}
