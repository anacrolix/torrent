package torrent

import (
	"net/http"

	"github.com/anacrolix/log"
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

var _ PeerImpl = (*webSeed)(nil)

func (ws *webSeed) onGotInfo(info *metainfo.Info) {
	ws.client.FileIndex = segments.NewIndex(common.LengthIterFromUpvertedFiles(info.UpvertedFiles()))
	ws.client.Info = info
}

func (ws *webSeed) PostCancel(r request) {
	ws.Cancel(r)
}

func (ws *webSeed) WriteInterested(interested bool) bool {
	return true
}

func (ws *webSeed) Cancel(r request) bool {
	ws.requests[r].Cancel()
	return true
}

func (ws *webSeed) intoSpec(r request) webseed.RequestSpec {
	return webseed.RequestSpec{ws.peer.t.requestOffset(r), int64(r.Length)}
}

func (ws *webSeed) Request(r request) bool {
	webseedRequest := ws.client.NewRequest(ws.intoSpec(r))
	ws.requests[r] = webseedRequest
	go ws.requestResultHandler(r, webseedRequest)
	return true
}

func (ws *webSeed) ConnectionFlags() string {
	return "WS"
}

func (ws *webSeed) Drop() {
}

func (ws *webSeed) UpdateRequests() {
	ws.peer.doRequestState()
}

func (ws *webSeed) Close() {}

func (ws *webSeed) requestResultHandler(r request, webseedRequest webseed.Request) {
	result := <-webseedRequest.Result
	ws.peer.t.cl.lock()
	defer ws.peer.t.cl.unlock()
	if result.Err != nil {
		log.Printf("webseed request rejected: %v", result.Err)
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
