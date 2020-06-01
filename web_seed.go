package torrent

import (
	"net/http"

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
	client webseed.Client
	peer   peer
}

type webseedClientEvent interface{}

type webseedRequestFailed struct {
	r   request
	err error
}

var _ PeerImpl = (*webSeed)(nil)

func (ws *webSeed) PostCancel(r request) {
	ws.Cancel(r)
}

func (ws *webSeed) WriteInterested(interested bool) bool {
	return true
}

func (ws *webSeed) Cancel(r request) bool {
	//panic("implement me")
	return true
}

func (ws *webSeed) Request(r request) bool {
	ws.client.Request(webseed.RequestSpec{ws.peer.t.requestOffset(r), int64(r.Length)})
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
