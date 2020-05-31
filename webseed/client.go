package webseed

import (
	"net/http"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

type RequestSpec = pp.RequestSpec

type Client struct {
	HttpClient *http.Client
	Url        string

	requests map[RequestSpec]request
}

type request struct {
	cancel func()
}

func (cl *Client) Request(r RequestSpec) {
	//cl.HttpClient.Do()
}
