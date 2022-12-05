package httpTrackerServer

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/tracker"
	httpTracker "github.com/anacrolix/torrent/tracker/http"
	udpTrackerServer "github.com/anacrolix/torrent/tracker/udp/server"
)

type Handler struct {
	AnnounceTracker udpTrackerServer.AnnounceTracker
}

func unmarshalQueryKeyToArray(w http.ResponseWriter, key string, query url.Values) (ret [20]byte, ok bool) {
	str := query.Get(key)
	if len(str) != len(ret) {
		http.Error(w, fmt.Sprintf("%v has wrong length", key), http.StatusBadRequest)
		return
	}
	copy(ret[:], str)
	ok = true
	return
}

func (me Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vs := r.URL.Query()
	var event tracker.AnnounceEvent
	err := event.UnmarshalText([]byte(vs.Get("event")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	infoHash, ok := unmarshalQueryKeyToArray(w, "info_hash", vs)
	if !ok {
		return
	}
	peerId, ok := unmarshalQueryKeyToArray(w, "peer_id", vs)
	if !ok {
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		log.Printf("error splitting remote port: %v", err)
		http.Error(w, "error determining your IP", http.StatusInternalServerError)
		return
	}
	addrPort, err := netip.ParseAddrPort(net.JoinHostPort(host, vs.Get("port")))
	err = me.AnnounceTracker.TrackAnnounce(r.Context(), tracker.AnnounceRequest{
		InfoHash: infoHash,
		PeerId:   peerId,
		Event:    event,
		Port:     addrPort.Port(),
	}, addrPort)
	if err != nil {
		log.Printf("error tracking announce: %v", err)
		http.Error(w, "error tracking announce", http.StatusInternalServerError)
		return
	}
	peers, err := me.AnnounceTracker.GetPeers(r.Context(), infoHash, tracker.GetPeersOpts{})
	if err != nil {
		log.Printf("error getting peers: %v", err)
		http.Error(w, "error getting peers", http.StatusInternalServerError)
		return
	}
	var resp httpTracker.HttpResponse
	resp.Interval = 5 * 60
	resp.Peers.Compact = true
	for _, peer := range peers {
		if peer.Addr().Is4() {
			resp.Peers.List = append(resp.Peers.List, tracker.Peer{
				IP:   peer.Addr().AsSlice(),
				Port: int(peer.Port()),
			})
		} else if peer.Addr().Is6() {
			resp.Peers6 = append(resp.Peers6, krpc.NodeAddr{
				IP:   peer.Addr().AsSlice(),
				Port: int(peer.Port()),
			})
		}
	}
	err = bencode.NewEncoder(w).Encode(resp)
	if err != nil {
		log.Printf("error encoding and writing response body: %v", err)
	}
}
