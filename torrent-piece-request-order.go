package torrent

func (t *Torrent) updatePieceRequestOrder(pieceIndex int) {
	t.cl.pieceRequestOrder[t.storage.Capacity].Update(
		t.pieceRequestOrderKey(pieceIndex),
		t.requestStrategyPieceOrderState(pieceIndex))
}
