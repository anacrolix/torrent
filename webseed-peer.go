package torrent

import (
	"fmt"
	"strings"

	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/webseed"
	"github.com/pkg/errors"
)

type webseedPeer struct {
	client   webseed.Client
	requests map[request]webseed.Request
	peer     Peer
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

func (ws *webseedPeer) _postCancel(r request) {
	ws.cancel(r)
}

func (ws *webseedPeer) writeInterested(interested bool) bool {
	return true
}

func (ws *webseedPeer) cancel(r request) bool {
	ws.requests[r].Cancel()
	return true
}

func (ws *webseedPeer) intoSpec(r request) webseed.RequestSpec {
	return webseed.RequestSpec{ws.peer.t.requestOffset(r), int64(r.Length)}
}

func (ws *webseedPeer) request(r request) bool {
	webseedRequest := ws.client.NewRequest(ws.intoSpec(r))
	ws.requests[r] = webseedRequest
	go ws.requestResultHandler(r, webseedRequest)
	return true
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

func (ws *webseedPeer) onClose() {}

func (ws *webseedPeer) requestResultHandler(r request, webseedRequest webseed.Request) {
	result := <-webseedRequest.Result
	ws.peer.t.cl.lock()
	defer ws.peer.t.cl.unlock()
	if result.Err != nil {
		ws.peer.logger.Printf("request %v rejected: %v", r, result.Err)
		// Always close for now. We need to filter out temporary errors, but this is a nightmare in
		// Go. Currently a bad webseed URL can starve out the good ones due to the chunk selection
		// algorithm.
		const closeOnAllErrors = false
		if closeOnAllErrors || strings.Contains(errors.Cause(result.Err).Error(), "unsupported protocol scheme") {
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
