package torrent

import (
	"github.com/anacrolix/missinggo/v2/panicif"

	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type requestStrategyInputCommon struct {
	maxUnverifiedBytes int64
}

func (r requestStrategyInputCommon) MaxUnverifiedBytes() int64 {
	return r.maxUnverifiedBytes
}

type torrentFromHashGetter interface {
	Get(shortInfohash) (*Torrent, bool)
}

type requestStrategyInputMultiTorrent struct {
	requestStrategyInputCommon
	torrents torrentFromHashGetter
	capFunc  storage.TorrentCapacity
}

func (r requestStrategyInputMultiTorrent) Torrent(ih metainfo.Hash) requestStrategy.Torrent {
	t, ok := r.torrents.Get(ih)
	panicif.False(ok)
	return requestStrategyTorrent{t}
}

func (r requestStrategyInputMultiTorrent) Capacity() (int64, bool) {
	return (*r.capFunc)()
}

// I don't think we need this for correctness purposes, but it must be faster to look up the Torrent
// input because it's locked to a given Torrent. It would be easy enough to drop in the
// multi-torrent version in this place and compare. TODO: With unique.Handle on infohash, this would
// not be necessary anymore. I don't think it's provided any performance benefit for some time now.
type requestStrategyInputSingleTorrent struct {
	requestStrategyInputCommon
	t *Torrent
}

func (r requestStrategyInputSingleTorrent) Torrent(_ metainfo.Hash) requestStrategy.Torrent {
	return requestStrategyTorrent{r.t}
}

func (r requestStrategyInputSingleTorrent) Capacity() (cap int64, capped bool) {
	return 0, false
}

var _ requestStrategy.Input = requestStrategyInputSingleTorrent{}

// getRequestStrategyInputCommon returns request strategy Input implementation common to all inputs.
func (cl *Client) getRequestStrategyInputCommon() requestStrategyInputCommon {
	return requestStrategyInputCommon{cl.config.MaxUnverifiedBytes}
}

func (t *Torrent) getRequestStrategyInput() requestStrategy.Input {
	return t.clientPieceRequestOrderKey().getRequestStrategyInput(t.cl)
}

// Wraps a Torrent to provide request-strategy.Torrent interface.
type requestStrategyTorrent struct {
	t *Torrent
}

func (r requestStrategyTorrent) Piece(i int) requestStrategy.Piece {
	return requestStrategyPiece{r.t.piece(i)}
}

func (r requestStrategyTorrent) PieceLength() int64 {
	return r.t.info.PieceLength
}

var _ requestStrategy.Torrent = requestStrategyTorrent{}

type requestStrategyPiece struct {
	p *Piece
}

func (r requestStrategyPiece) CountUnverified() bool {
	return r.p.hashing || r.p.marking || r.p.queuedForHash()
}

func (r requestStrategyPiece) Request() bool {
	return r.p.effectivePriority() > PiecePriorityNone
}

var _ requestStrategy.Piece = requestStrategyPiece{}
