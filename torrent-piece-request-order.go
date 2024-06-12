package torrent

import (
	g "github.com/anacrolix/generics"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

func (t *Torrent) updatePieceRequestOrderPiece(pieceIndex int, lock bool) {
	if t.storage == nil {
		return
	}
	pro := t.clientPieceRequestOrder
	if pro == nil {
		return
	}
	key := t.pieceRequestOrderKey(pieceIndex)
	if t.hasStorageCap() {
		pro.Update(key, t.requestStrategyPieceOrderState(pieceIndex, lock))
		return
	}
	//pending := !t.ignorePieceForRequests(pieceIndex)
	//if pending {
	pro.Add(key, t.requestStrategyPieceOrderState(pieceIndex, lock))
	//} else {
	//	pro.Delete(key)
	//}
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
	t.clientPieceRequestOrder = nil
}

func (t *Torrent) initPieceRequestOrder(lockClient bool) {
	if t.storage == nil {
		return
	}

	if lockClient {
		t.cl.lock()
		defer t.cl.unlock()
	}

	g.MakeMapIfNil(&t.cl.pieceRequestOrder)
	key := t.clientPieceRequestOrderKey()

	cpro := t.cl.pieceRequestOrder
	if cpro[key] == nil {
		cpro[key] = request_strategy.NewPieceOrder(request_strategy.NewAjwernerBtree(), t.numPieces())
	}
	t.clientPieceRequestOrder = cpro[key]
}

func (t *Torrent) addRequestOrderPiece(i int, lock bool) {
	if t.storage == nil {
		return
	}
	pro := t.getPieceRequestOrder()
	key := t.pieceRequestOrderKey(i)
	if t.hasStorageCap() || !t.ignorePieceForRequests(i, lock) {
		pro.Add(key, t.requestStrategyPieceOrderState(i, lock))
	}
}

func (t *Torrent) getPieceRequestOrder() *request_strategy.PieceRequestOrder {
	return t.clientPieceRequestOrder
}
