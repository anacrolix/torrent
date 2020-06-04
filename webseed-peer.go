package torrent

import (
	"fmt"

	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/webseed"
)

type webseedPeer struct {
	client   webseed.Client
	requests map[request]webseed.Request
	peer     peer
}

var _ peerImpl = (*webseedPeer)(nil)

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

// TODO: This is called when banning peers. Perhaps we want to be able to ban webseeds too.
func (ws *webseedPeer) drop() {}

func (ws *webseedPeer) updateRequests() {
	ws.peer.doRequestState()
}

func (ws *webseedPeer) _close() {}

func (ws *webseedPeer) requestResultHandler(r request, webseedRequest webseed.Request) {
	result := <-webseedRequest.Result
	ws.peer.t.cl.lock()
	defer ws.peer.t.cl.unlock()
	if result.Err != nil {
		ws.peer.logger.Printf("request %v rejected: %v", r, result.Err)
		ws.peer.remoteRejectedRequest(r)
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
