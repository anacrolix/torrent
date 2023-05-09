package torrent

import (
	"github.com/anacrolix/chansync"
)

type utHolepunchRendezvous struct {
	relays     map[*PeerConn]struct{}
	gotConnect chansync.SetOnce
	relayCond  chansync.BroadcastCond
}
