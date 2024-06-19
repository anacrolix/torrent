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

func (cl *Client) getRequestStrategyInputCommon() requestStrategyInputCommon {
	return requestStrategyInputCommon{cl.config.MaxUnverifiedBytes}
}

// Returns what is necessary to run request_strategy.GetRequestablePieces for primaryTorrent.
func (cl *Client) getRequestStrategyInput(primaryTorrent *Torrent) (input request_strategy.Input) {
	if primaryTorrent.storage.Capacity == nil {
		return requestStrategyInputSingleTorrent{
			requestStrategyInputCommon: cl.getRequestStrategyInputCommon(),
			t:                          primaryTorrent,
		}
	} else {
		return requestStrategyInputMultiTorrent{
			requestStrategyInputCommon: cl.getRequestStrategyInputCommon(),
			torrents:                   cl.torrents,
			capFunc:                    primaryTorrent.storage.Capacity,
		}
	}
}

func (t *Torrent) getRequestStrategyInput() request_strategy.Input {
	return t.cl.getRequestStrategyInput(t)
}

type requestStrategyTorrent struct {
	t *Torrent
}

func (r requestStrategyTorrent) Piece(i int, lockTorrent bool) request_strategy.Piece {
	return (*requestStrategyPiece)(r.t.piece(i, lockTorrent))
}

func (r requestStrategyTorrent) PieceLength() int64 {
	return r.t.info.PieceLength
}

func (r requestStrategyTorrent) GetPieceRequestOrder() *request_strategy.PieceRequestOrder {
	return r.t.getPieceRequestOrder()
}

func (r requestStrategyTorrent) RLock() {
	r.t.mu.RLock()
}

func (r requestStrategyTorrent) RUnlock() {
	r.t.mu.RUnlock()
}

var _ request_strategy.Torrent = requestStrategyTorrent{}

type requestStrategyPiece Piece

func (r *requestStrategyPiece) Request(lockTorrent bool) bool {
	return !r.t.ignorePieceForRequests(r.index, lockTorrent)
}

func (r *requestStrategyPiece) NumPendingChunks(lockTorrent bool) int {
	return int(r.t.pieceNumPendingChunks(r.index, lockTorrent))
}

var _ request_strategy.Piece = (*requestStrategyPiece)(nil)
