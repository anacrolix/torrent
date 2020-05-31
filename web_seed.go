package torrent

import (
	"net/http"
)

type webSeed struct {
	peer       *peer
	httpClient *http.Client
	url        string
}

func (ws *webSeed) PostCancel(r request) {
	panic("implement me")
}

func (ws *webSeed) WriteInterested(interested bool) bool {
	return true
}

func (ws *webSeed) Cancel(r request) bool {
	panic("implement me")
}

func (ws *webSeed) Request(r request) bool {
	panic("implement me")
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
