package torrent

import (
	"expvar"

	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/internal/bytesx"
)

const (
	maxRequestsGrace = 5 // grace requests above the limit before we start rejecting
	defaultChunkSize = 16 * bytesx.KiB
)

func defaultPeerExtensionBytes() pp.ExtensionBits {
	return pp.NewExtensionBits(pp.ExtensionBitDHT, pp.ExtensionBitExtended, pp.ExtensionBitFast)
}

// I could move a lot of these counters to their own file, but I suspect they
// may be attached to a Client someday.
var (
	metrics = expvar.NewMap("torrent")

	completedHandshakeConnectionFlags = expvar.NewMap("completedHandshakeConnectionFlags")

	requestedChunkLengths = expvar.NewMap("requestedChunkLengths")
)
