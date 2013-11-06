package tracker

import (
	"net"
	"net/url"
)

type AnnounceRequest struct {
	InfoHash   [20]byte
	PeerId     [20]byte
	Downloaded int64
	Left       int64
	Uploaded   int64
	Event      AnnounceEvent
	IPAddress  int32
	Key        int32
	NumWant    int32
	Port       int16
}

type AnnounceResponse struct {
	Interval int32
	Leechers int32
	Seeders  int32
	Peers    []Peer
}

type AnnounceEvent int32

type Peer struct {
	IP   net.IP
	Port int
}

const (
	None AnnounceEvent = iota
)

type Client interface {
	Announce(*AnnounceRequest) (AnnounceResponse, error)
}

var schemes = make(map[string]func(*url.URL) Client)

func RegisterClientScheme(scheme string, newFunc func(*url.URL) Client) {
}

func New(url *url.URL) Client {
	return schemes[url.Scheme](url)
}
