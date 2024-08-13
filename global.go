package torrent

import (
	"crypto"
	"expvar"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	pieceHash        = crypto.SHA1
	defaultChunkSize = 0x4000 // 16KiB

	// Arbitrary maximum of "metadata_size" (see https://www.bittorrent.org/beps/bep_0009.html)
	// libtorrent-rasterbar uses 4MiB at last check. TODO: Add links to values used by other
	// implementations here. I saw 14143527 in the metainfo for
	// 3597f16e239aeb8f8524a1a1c4e4725a0a96b470. Large values for legitimate torrents should be
	// recorded here for consideration.
	maxMetadataSize uint32 = 16 * 1024 * 1024
)

func defaultPeerExtensionBytes() PeerExtensionBits {
	return pp.NewPeerExtensionBytes(pp.ExtensionBitDht, pp.ExtensionBitLtep, pp.ExtensionBitFast)
}

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

	// Track the effectiveness of Torrent.connPieceInclinationPool.
	pieceInclinationsReused = expvar.NewInt("pieceInclinationsReused")
	pieceInclinationsNew    = expvar.NewInt("pieceInclinationsNew")
	pieceInclinationsPut    = expvar.NewInt("pieceInclinationsPut")

	concurrentChunkWrites = expvar.NewInt("torrentConcurrentChunkWrites")
)
