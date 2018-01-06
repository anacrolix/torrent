package torrent

import (
	"encoding/hex"
)

type peerID [20]byte

func (me peerID) String() string {
	if me[0] == '-' && me[7] == '-' {
		return string(me[:8]) + hex.EncodeToString(me[8:])
	}
	return hex.EncodeToString(me[:])
}
