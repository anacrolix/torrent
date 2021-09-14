package torrent

import (
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
)

func makeCancelMessage(r types.Request) pp.Message {
	return pp.MakeCancelMessage(r.Index, r.Begin, r.Length)
}
