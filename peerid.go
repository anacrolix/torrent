package torrent

import (
	"encoding/hex"
)

type PeerID [20]byte

func (me PeerID) String() string {
	if me[0] == '-' && me[7] == '-' {
		return string(me[:8]) + hex.EncodeToString(me[8:])
	}
	return hex.EncodeToString(me[:])
}
