package httpTrackerServer

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/tracker"
	httpTracker "github.com/anacrolix/torrent/tracker/http"
	trackerServer "github.com/anacrolix/torrent/tracker/server"
)

type Handler struct {
	Announce *trackerServer.AnnounceHandler
	// Called to derive an announcer's IP if non-nil. If not specified, the Request.RemoteAddr is
	// used. Necessary for instances running behind reverse proxies for example.
	RequestHost func(r *http.Request) (netip.Addr, error)
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

// Returns false if there was an error and it was served.
func (me Handler) requestHostAddr(r *http.Request) (_ netip.Addr, err error) {
	if me.RequestHost != nil {
		return me.RequestHost(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return
	}
	return netip.ParseAddr(host)
}

var requestHeadersLogger = slog.Default().WithGroup("request").WithGroup("headers")

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
	requestHeadersLogger.Debug("request", "remoteAddr", r.RemoteAddr, "header", r.Header)
	addr, err := me.requestHostAddr(r)
	if err != nil {
		slog.Error("error getting requester IP", "err", err)
		http.Error(w, "error determining your IP", http.StatusBadGateway)
		return
	}
	portU64, _ := strconv.ParseUint(vs.Get("port"), 0, 16)
	addrPort := netip.AddrPortFrom(addr, uint16(portU64))
	left, err := strconv.ParseInt(vs.Get("left"), 0, 64)
	if err != nil {
		left = -1
	}
	res := me.Announce.Serve(
		r.Context(),
		tracker.AnnounceRequest{
			InfoHash: infoHash,
			PeerId:   peerId,
			Event:    event,
			Port:     addrPort.Port(),
			NumWant:  -1,
			Left:     left,
		},
		addrPort,
		trackerServer.GetPeersOpts{
			MaxCount: generics.Some[uint](200),
		},
	)
	err = res.Err
	if err != nil {
		slog.Error("error serving announce", "err", err)
		http.Error(w, "error handling announce", http.StatusInternalServerError)
		return
	}
	var resp httpTracker.HttpResponse
	resp.Incomplete = res.Leechers.Value
	resp.Complete = res.Seeders.Value
	resp.Interval = res.Interval.UnwrapOr(5 * 60)
	resp.Peers.Compact = true
	for _, peer := range res.Peers {
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
		slog.Error("error encoding and writing response body", "err", err)
	}
}
