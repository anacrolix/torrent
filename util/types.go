package util

import (
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"github.com/anacrolix/libtorgo/bencode"
)

type CompactPeers []tracker.CompactPeer

func (me *CompactPeers) UnmarshalBencode(bb []byte) (err error) {
	var b []byte
	err = bencode.Unmarshal(bb, &b)
	if err != nil {
		return
	}
	err = me.UnmarshalBinary(b)
	return
}

func (me *CompactPeers) UnmarshalBinary(b []byte) (err error) {
	for i := 0; i < len(b); i += 6 {
		var p tracker.CompactPeer
		err = p.UnmarshalBinary([]byte(b[i : i+6]))
		if err != nil {
			return
		}
		*me = append(*me, p)
	}
	return
}
