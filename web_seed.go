package torrent

import (
	"fmt"
	"net/http"

	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/webseed"
)

type httpRequestResult struct {
	resp *http.Response
	err  error
}

type requestPart struct {
	req    *http.Request
	e      segments.Extent
	result chan httpRequestResult
}

type webseedRequest struct {
	cancel func()
}

type webSeed struct {
	client   webseed.Client
	requests map[request]webseed.Request
	peer     peer
}

var _ peerImpl = (*webSeed)(nil)

func (ws *webSeed) String() string {
	return fmt.Sprintf("webseed peer for %q", ws.client.Url)
}

func (ws *webSeed) onGotInfo(info *metainfo.Info) {
	ws.client.FileIndex = segments.NewIndex(common.LengthIterFromUpvertedFiles(info.UpvertedFiles()))
	ws.client.Info = info
}

func (ws *webSeed) _postCancel(r request) {
	ws.cancel(r)
}

func (ws *webSeed) writeInterested(interested bool) bool {
	return true
}

func (ws *webSeed) cancel(r request) bool {
	ws.requests[r].Cancel()
	return true
}

func (ws *webSeed) intoSpec(r request) webseed.RequestSpec {
	return webseed.RequestSpec{ws.peer.t.requestOffset(r), int64(r.Length)}
}

func (ws *webSeed) request(r request) bool {
	webseedRequest := ws.client.NewRequest(ws.intoSpec(r))
	ws.requests[r] = webseedRequest
	go ws.requestResultHandler(r, webseedRequest)
	return true
}

func (ws *webSeed) connectionFlags() string {
	return "WS"
}

func (ws *webSeed) drop() {
}

func (ws *webSeed) updateRequests() {
	ws.peer.doRequestState()
}

func (ws *webSeed) _close() {}

func (ws *webSeed) requestResultHandler(r request, webseedRequest webseed.Request) {
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
