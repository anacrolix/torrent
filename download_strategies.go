package torrent

import (
	"io"

	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

type DownloadStrategy interface {
	// Tops up the outgoing pending requests.
	FillRequests(*torrent, *connection)
	TorrentStarted(*torrent)
	TorrentStopped(*torrent)
	DeleteRequest(*torrent, request)
	TorrentPrioritize(t *torrent, off, _len int64)
	TorrentGotChunk(*torrent, request)
	TorrentGotPiece(t *torrent, piece int)
	WriteStatus(w io.Writer)
	AssertNotRequested(*torrent, request)
	PendingData(*torrent) bool
}

type DefaultDownloadStrategy struct{}

func (me *DefaultDownloadStrategy) PendingData(t *torrent) bool {
	return !t.haveAllPieces()
}

func (me *DefaultDownloadStrategy) AssertNotRequested(t *torrent, r request) {}

func (me *DefaultDownloadStrategy) WriteStatus(w io.Writer) {}

func (s *DefaultDownloadStrategy) FillRequests(t *torrent, c *connection) {
	if c.Interested {
		if c.PeerChoked {
			return
		}
		if len(c.Requests) > c.requestsLowWater {
			return
		}
	}
	addRequest := func(req request) (again bool) {
		return c.Request(req)
	}
	for e := c.pieceRequestOrder.First(); e != nil; e = e.Next() {
		pieceIndex := e.Piece()
		if !c.PeerHasPiece(pp.Integer(pieceIndex)) {
			panic("piece in request order but peer doesn't have it")
		}
		if !t.wantPiece(pieceIndex) {
			panic("unwanted piece in connection request order")
		}
		piece := t.Pieces[pieceIndex]
		for _, cs := range piece.shuffledPendingChunkSpecs() {
			r := request{pp.Integer(pieceIndex), cs}
			if !addRequest(r) {
				return
			}
		}
	}
	return
}

func (s *DefaultDownloadStrategy) TorrentStarted(t *torrent) {}

func (s *DefaultDownloadStrategy) TorrentStopped(t *torrent) {
}

func (s *DefaultDownloadStrategy) DeleteRequest(t *torrent, r request) {
}

func (me *DefaultDownloadStrategy) TorrentGotChunk(t *torrent, c request)      {}
func (me *DefaultDownloadStrategy) TorrentGotPiece(t *torrent, piece int)      {}
func (*DefaultDownloadStrategy) TorrentPrioritize(t *torrent, off, _len int64) {}
