package torrent

import (
	"crypto"

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
