package torrent

import (
	"expvar"
)

func init() {
	torrent.Set("peers supporting extension", &peersSupportingExtension)
	torrent.Set("chunks received", &ChunksReceived)
}

// I could move a lot of these counters to their own file, but I suspect they
// may be attached to a Client someday.
var (
	torrent                  = expvar.NewMap("torrent")
	peersSupportingExtension expvar.Map
	// This could move at any time. It contains counts of chunks received and the conditions they
	// were received.
	ChunksReceived expvar.Map

	pieceHashedCorrect    = expvar.NewInt("pieceHashedCorrect")
	pieceHashedNotCorrect = expvar.NewInt("pieceHashedNotCorrect")

	completedHandshakeConnectionFlags = expvar.NewMap("completedHandshakeConnectionFlags")
	// Count of connections to peer with same client ID.
	connsToSelf        = expvar.NewInt("connsToSelf")
	receivedKeepalives = expvar.NewInt("receivedKeepalives")
	// Requests received for pieces we don't have.
	requestsReceivedForMissingPieces = expvar.NewInt("requestsReceivedForMissingPieces")
	requestedChunkLengths            = expvar.NewMap("requestedChunkLengths")

	messageTypesReceived = expvar.NewMap("messageTypesReceived")

	concurrentChunkWrites = expvar.NewInt("torrentConcurrentChunkWrites")
)
