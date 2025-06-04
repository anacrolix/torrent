package torrent

import (
	"unique"

	"github.com/anacrolix/torrent/metainfo"
	requestStrategy "github.com/anacrolix/torrent/request-strategy"
)

type (
	webseedHostKey       string
	webseedHostKeyHandle = unique.Handle[webseedHostKey]
)

/*
- Go through all the requestable pieces in order of priority, availability, whether there are peer requests, partial, infohash.
- For each piece calculate files involved. Record each file not seen before and the piece index.
- Cancel any outstanding requests that don't match a final file/piece-index pair.
- Initiate missing requests that fit into the available limits.

This was a globally aware webseed requestor algorithm that is probably going to be abandoned.
*/
func (cl *Client) abandonedUpdateWebSeedRequests() {
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

func (cl *Client) updateWebSeedRequests(reason updateRequestReason) {
	for t := range cl.torrents {
		for _, p := range t.webSeeds {
			p.peer.updateRequestsWithReason(reason)
		}
	}
}
