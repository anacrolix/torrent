package torrent

import (
	g "github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/metainfo"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/storage"
)

type requestStrategyInputCommon struct {
	maxUnverifiedBytes int64
}

func (r requestStrategyInputCommon) MaxUnverifiedBytes() int64 {
	return r.maxUnverifiedBytes
}

type requestStrategyInputMultiTorrent struct {
	requestStrategyInputCommon
	torrents map[metainfo.Hash]*Torrent
	capFunc  storage.TorrentCapacity
}

func (r requestStrategyInputMultiTorrent) Torrent(ih metainfo.Hash) request_strategy.Torrent {
	return requestStrategyTorrent{g.MapMustGet(r.torrents, ih)}
}

func (r requestStrategyInputMultiTorrent) Capacity() (int64, bool) {
	return (*r.capFunc)()
}

// I don't think we need this for correctness purposes, but it must be faster to look up the Torrent
// input because it's locked to a given Torrent. It would be easy enough to drop in the
// multi-torrent version in this place and compare.
type requestStrategyInputSingleTorrent struct {
	requestStrategyInputCommon
	t *Torrent
}

func (r requestStrategyInputSingleTorrent) Torrent(_ metainfo.Hash) request_strategy.Torrent {
	return requestStrategyTorrent{r.t}
}

func (r requestStrategyInputSingleTorrent) Capacity() (cap int64, capped bool) {
	return 0, false
}

var _ request_strategy.Input = requestStrategyInputSingleTorrent{}

// getRequestStrategyInputCommon returns request strategy Input implementation common to all inputs.
func (cl *Client) getRequestStrategyInputCommon() requestStrategyInputCommon {
	return requestStrategyInputCommon{cl.config.MaxUnverifiedBytes}
}

func (t *Torrent) getRequestStrategyInput() request_strategy.Input {
	return t.clientPieceRequestOrderKey().getRequestStrategyInput(t.cl)
}

// Wraps a Torrent to provide request-strategy.Torrent interface.
type requestStrategyTorrent struct {
	t *Torrent
}

func (r requestStrategyTorrent) Piece(i int) request_strategy.Piece {
	return requestStrategyPiece{r.t.piece(i)}
}

func (r requestStrategyTorrent) PieceLength() int64 {
	return r.t.info.PieceLength
}

var _ request_strategy.Torrent = requestStrategyTorrent{}

type requestStrategyPiece struct {
	p *Piece
}

func (r requestStrategyPiece) CountUnverified() bool {
	return r.p.hashing || r.p.marking || r.p.queuedForHash()
}

func (r requestStrategyPiece) Request() bool {
	return !r.p.ignoreForRequests() && r.p.purePriority() != PiecePriorityNone
}

var _ request_strategy.Piece = requestStrategyPiece{}
