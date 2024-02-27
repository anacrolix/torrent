package torrent

import (
	g "github.com/anacrolix/generics"

	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

func (t *Torrent) updatePieceRequestOrderPiece(pieceIndex int) {
	if t.storage == nil {
		return
	}
	pro, ok := t.cl.pieceRequestOrder[t.clientPieceRequestOrderKey()]
	if !ok {
		return
	}
	key := t.pieceRequestOrderKey(pieceIndex)
	if t.hasStorageCap() {
		pro.Update(key, t.requestStrategyPieceOrderState(pieceIndex))
		return
	}
	pending := !t.ignorePieceForRequests(pieceIndex)
	if pending {
		pro.Add(key, t.requestStrategyPieceOrderState(pieceIndex))
	} else {
		pro.Delete(key)
	}
}

func (t *Torrent) clientPieceRequestOrderKey() interface{} {
	if t.storage.Capacity == nil {
		return t
	}
	return t.storage.Capacity
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
