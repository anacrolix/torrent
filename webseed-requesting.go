package torrent

import (
	"github.com/anacrolix/torrent/metainfo"
	requestStrategy "github.com/anacrolix/torrent/request-strategy"
)

/*
- Go through all the requestable pieces in order of priority, availability, whether there are peer requests, partial, infohash.
- For each piece calculate files involved. Record each file not seen before and the piece index.
- Cancel any outstanding requests that don't match a final file/piece-index pair.
- Initiate missing requests that fit into the available limits.
*/
func (cl *Client) updateWebSeedRequests() {
	for key, value := range cl.pieceRequestOrder {
		input := key.getRequestStrategyInput(cl)
		requestStrategy.GetRequestablePieces(
			input,
			value.pieces,
			func(ih metainfo.Hash, pieceIndex int, orderState requestStrategy.PieceRequestOrderState) bool {
				return true
			},
		)
	}
}
