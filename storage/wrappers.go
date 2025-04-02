package storage

import (
	"github.com/james-lawrence/torrent/metainfo"
)

type Client struct {
	ci ClientImpl
}

func NewClient(cl ClientImpl) *Client {
	return &Client{cl}
}

func (cl Client) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	t, err := cl.ci.OpenTorrent(info, infoHash)
	return t, err
}

func NewTorrent(ti TorrentImpl) TorrentImpl {
	return ti
}
