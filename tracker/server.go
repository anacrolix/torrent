package tracker

import (
	"context"
	"net/netip"

	"github.com/anacrolix/torrent/tracker/udp"
)

// This is reserved for stuff like filtering by IP version, avoiding an announcer's IP or key,
// limiting return count, etc.
type GetPeersOpts struct{}

type InfoHash = [20]byte

type PeerInfo struct {
	AnnounceAddr
}

type AnnounceAddr = netip.AddrPort

type AnnounceTracker interface {
	TrackAnnounce(ctx context.Context, req udp.AnnounceRequest, addr AnnounceAddr) error
	Scrape(ctx context.Context, infoHashes []InfoHash) ([]udp.ScrapeInfohashResult, error)
	GetPeers(ctx context.Context, infoHash InfoHash, opts GetPeersOpts) ([]PeerInfo, error)
}

//
//type Server struct {
//	AnnounceTracker AnnounceTracker
//}
//
//func (me Server) HandleAnnounce(req udp.AnnounceRequest, sourceAddr AnnounceAddr) error {
//
//}
