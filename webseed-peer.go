package torrent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/webseed"
)

type webseedPeer struct {
	client         webseed.Client
	activeRequests map[Request]webseed.Request
	requesterCond  sync.Cond
	peer           Peer
}

var _ peerImpl = (*webseedPeer)(nil)

func (me *webseedPeer) connStatusString() string {
	return me.client.Url
}

func (ws *webseedPeer) String() string {
	return fmt.Sprintf("webseed peer for %q", ws.client.Url)
}

func (ws *webseedPeer) onGotInfo(info *metainfo.Info) {
	ws.client.FileIndex = segments.NewIndex(common.LengthIterFromUpvertedFiles(info.UpvertedFiles()))
	ws.client.Info = info
}

func (ws *webseedPeer) _postCancel(r Request) {
	ws.cancel(r)
}

func (ws *webseedPeer) writeInterested(interested bool) bool {
	return true
}

func (ws *webseedPeer) cancel(r Request) bool {
	active, ok := ws.activeRequests[r]
	if !ok {
		return false
	}
	active.Cancel()
	return true
}

func (ws *webseedPeer) intoSpec(r Request) webseed.RequestSpec {
	return webseed.RequestSpec{ws.peer.t.requestOffset(r), int64(r.Length)}
}

func (ws *webseedPeer) request(r Request) bool {
	ws.requesterCond.Signal()
	return true
}

func (ws *webseedPeer) doRequest(r Request) {
	webseedRequest := ws.client.NewRequest(ws.intoSpec(r))
	ws.activeRequests[r] = webseedRequest
	func() {
		ws.requesterCond.L.Unlock()
		defer ws.requesterCond.L.Lock()
		ws.requestResultHandler(r, webseedRequest)
	}()
	delete(ws.activeRequests, r)
}

func (ws *webseedPeer) requester() {
	ws.requesterCond.L.Lock()
	defer ws.requesterCond.L.Unlock()
start:
	for !ws.peer.closed.IsSet() {
		for r := range ws.peer.requests {
			if _, ok := ws.activeRequests[r]; ok {
				continue
			}
			ws.doRequest(r)
			goto start
		}
		ws.requesterCond.Wait()
	}
}

func (ws *webseedPeer) connectionFlags() string {
	return "WS"
}

// TODO: This is called when banning peers. Perhaps we want to be able to ban webseeds too. We could
// return bool if this is even possible, and if it isn't, skip to the next drop candidate.
func (ws *webseedPeer) drop() {}

func (ws *webseedPeer) updateRequests() {
	ws.peer.doRequestState()
}

func (ws *webseedPeer) onClose() {
	ws.peer.logger.Print("closing")
	for _, r := range ws.activeRequests {
		r.Cancel()
	}
	ws.requesterCond.Broadcast()
}

func (ws *webseedPeer) requestResultHandler(r Request, webseedRequest webseed.Request) {
	result := <-webseedRequest.Result
	ws.peer.t.cl.lock()
	defer ws.peer.t.cl.unlock()
	if result.Err != nil {
		if !errors.Is(result.Err, context.Canceled) {
			ws.peer.logger.Printf("Request %v rejected: %v", r, result.Err)
		}
		// Always close for now. We need to filter out temporary errors, but this is a nightmare in
		// Go. Currently a bad webseed URL can starve out the good ones due to the chunk selection
		// algorithm.
		const closeOnAllErrors = false
		if closeOnAllErrors || strings.Contains(result.Err.Error(), "unsupported protocol scheme") {
			ws.peer.close()
		} else {
			ws.peer.remoteRejectedRequest(r)
		}
	} else {
		err := ws.peer.receiveChunk(&pp.Message{
			Type:  pp.Piece,
			Index: r.Index,
			Begin: r.Begin,
			Piece: result.Bytes,
		})
		if err != nil {
			panic(err)
		}
	}
}
