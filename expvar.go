package torrent

import (
	"expvar"
	"strconv"
	"sync"
)

func init() {
	torrent.Set("peers supporting extension", &peersSupportingExtension)
	torrent.Set("chunks received", &ChunksReceived)
}

// I could move a lot of these counters to their own file, but I suspect they
// may be attached to a Client someday.
var (
	torrent                  = expvar.NewMap("torrent")
	peersSupportingExtension boundedExpvarMap
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

// Peers can send arbitrary extension names, and as many as fit in a message.
const maxPeerExtensionNames = 256

// Most distinct keys a boundedExpvarMap holds before counting under "other".
const maxBoundedExpvarMapKeys = 1000

// An expvar.Map whose keys come from peers. It holds a bounded number of keys so peers can't grow
// it without limit.
type boundedExpvarMap struct {
	expvar.Map
	mu   sync.Mutex
	keys map[string]struct{}
}

func (me *boundedExpvarMap) add(key string) {
	// expvar.Var.String must produce valid JSON. "ut_payme\xeet_address" was being entered here
	// which caused problems later when unmarshalling.
	key = strconv.Quote(key)
	me.mu.Lock()
	if _, ok := me.keys[key]; !ok {
		if len(me.keys) >= maxBoundedExpvarMapKeys {
			key = "other"
		} else {
			if me.keys == nil {
				me.keys = make(map[string]struct{})
			}
			me.keys[key] = struct{}{}
		}
	}
	me.mu.Unlock()
	me.Map.Add(key, 1)
}
