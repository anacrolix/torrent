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
	// This value is 2x what libtorrent-rasterbar uses, which should be plenty
	maxMetadataSize uint32 = 8 * 1024 * 1024
)

// These are our extended message IDs. Peers will use these values to
// select which extension a message is intended for.
const (
	metadataExtendedId = iota + 1 // 0 is reserved for deleting keys
	pexExtendedId
)

func defaultPeerExtensionBytes() PeerExtensionBits {
	return pp.NewPeerExtensionBytes(pp.ExtensionBitDHT, pp.ExtensionBitExtended, pp.ExtensionBitFast)
}

func init() {
	chunksReceived = newExpvarHookMap("chunks_received")
	torrent.Set("peers supporting extension", &peersSupportingExtension)
	torrent.Set("chunks received", chunksReceived)
}

// I could move a lot of these counters to their own file, but I suspect they
// may be attached to a Client someday.
var (
	torrent                  = newExpvarHookMap("torrent")
	peersSupportingExtension expvar.Map
	chunksReceived           *expvarHookMap

	pieceHashedCorrect    = newExpvarHookInt("pieceHashedCorrect")
	pieceHashedNotCorrect = newExpvarHookInt("pieceHashedNotCorrect")

	completedHandshakeConnectionFlags = newExpvarHookMap("completedHandshakeConnectionFlags")
	// Count of connections to peer with same client ID.
	connsToSelf        = newExpvarHookInt("connsToSelf")
	receivedKeepalives = newExpvarHookInt("receivedKeepalives")
	// Requests received for pieces we don't have.
	requestsReceivedForMissingPieces = newExpvarHookInt("requestsReceivedForMissingPieces")
	requestedChunkLengths            = newExpvarHookMap("requestedChunkLengths")

	messageTypesReceived = newExpvarHookMap("messageTypesReceived")

	// Track the effectiveness of Torrent.connPieceInclinationPool.
	pieceInclinationsReused = expvar.NewInt("pieceInclinationsReused")
	pieceInclinationsNew    = expvar.NewInt("pieceInclinationsNew")
	pieceInclinationsPut    = expvar.NewInt("pieceInclinationsPut")

	concurrentChunkWrites = newExpvarHookInt("torrentConcurrentChunkWrites")
)
