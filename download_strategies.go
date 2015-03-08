package torrent

import (
	"io"

	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

type downloadStrategy interface {
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

type defaultDownloadStrategy struct{}

func (me *defaultDownloadStrategy) PendingData(t *torrent) bool {
	return !t.haveAllPieces()
}

func (me *defaultDownloadStrategy) AssertNotRequested(t *torrent, r request) {}

func (me *defaultDownloadStrategy) WriteStatus(w io.Writer) {}

func (s *defaultDownloadStrategy) FillRequests(t *torrent, c *connection) {
	if c.Interested {
		if c.PeerChoked {
			return
		}
		if len(c.Requests) > c.requestsLowWater {
			return
		}
	}
	addRequest := func(req request) (again bool) {
		if len(c.Requests) >= 32 {
			return false
		}
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

func (s *defaultDownloadStrategy) TorrentStarted(t *torrent) {}

func (s *defaultDownloadStrategy) TorrentStopped(t *torrent) {
}

func (s *defaultDownloadStrategy) DeleteRequest(t *torrent, r request) {
}

func (me *defaultDownloadStrategy) TorrentGotChunk(t *torrent, c request)      {}
func (me *defaultDownloadStrategy) TorrentGotPiece(t *torrent, piece int)      {}
func (*defaultDownloadStrategy) TorrentPrioritize(t *torrent, off, _len int64) {}
