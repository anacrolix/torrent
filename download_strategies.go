package torrent

import (
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

type DownloadStrategy interface {
	FillRequests(t *torrent, c *connection)
	TorrentStarted(t *torrent)
	TorrentStopped(t *torrent)
	DeleteRequest(t *torrent, r request)
}

type DefaultDownloadStrategy struct {
	heat map[*torrent]map[request]int
}

func (s *DefaultDownloadStrategy) FillRequests(t *torrent, c *connection) {
	if c.Interested {
		if c.PeerChoked {
			return
		}
		if len(c.Requests) >= (c.PeerMaxRequests+1)/2 {
			return
		}
	}
	th := s.heat[t]
	addRequest := func(req request) (again bool) {
		piece := t.Pieces[req.Index]
		if piece.Hashing || piece.QueuedForHash {
			// We can't be sure we want this.
			return true
		}
		if piece.Complete() {
			// We already have this.
			return true
		}
		if c.RequestPending(req) {
			return true
		}
		again = c.Request(req)
		if c.RequestPending(req) {
			th[req]++
		}
		return
	}
	// First request prioritized chunks.
	for e := t.Priorities.Front(); e != nil; e = e.Next() {
		if !addRequest(e.Value.(request)) {
			return
		}
	}
	// Then finish off incomplete pieces in order of bytes remaining.
	for _, heatThreshold := range []int{1, 4, 15, 60} {
		for e := t.PiecesByBytesLeft.Front(); e != nil; e = e.Next() {
			pieceIndex := pp.Integer(e.Value.(int))
			for _, chunkSpec := range t.Pieces[pieceIndex].shuffledPendingChunkSpecs() {
				// for chunkSpec := range t.Pieces[pieceIndex].PendingChunkSpecs {
				r := request{pieceIndex, chunkSpec}
				if th[r] >= heatThreshold {
					continue
				}
				if !addRequest(r) {
					return
				}
			}
		}
	}
}

func (s *DefaultDownloadStrategy) TorrentStarted(t *torrent) {
	if s.heat[t] != nil {
		panic("torrent already started")
	}
	if s.heat == nil {
		s.heat = make(map[*torrent]map[request]int, 10)
	}
	s.heat[t] = make(map[request]int, t.ChunkCount())
}

func (s *DefaultDownloadStrategy) TorrentStopped(t *torrent) {
	if _, ok := s.heat[t]; !ok {
		panic("torrent not yet started")
	}
	delete(s.heat, t)
}

func (s *DefaultDownloadStrategy) DeleteRequest(t *torrent, r request) {
	m := s.heat[t]
	if m[r] <= 0 {
		panic("no pending requests")
	}
	m[r]--
}

type ResponsiveDownloadStrategy struct {
	// How many bytes to preemptively download starting at the beginning of
	// the last piece read for a given torrent.
	Readahead int
}

func (ResponsiveDownloadStrategy) TorrentStarted(*torrent)         {}
func (ResponsiveDownloadStrategy) TorrentStopped(*torrent)         {}
func (ResponsiveDownloadStrategy) DeleteRequest(*torrent, request) {}

func (me *ResponsiveDownloadStrategy) FillRequests(t *torrent, c *connection) {
	for e := t.Priorities.Front(); e != nil; e = e.Next() {
		req := e.Value.(request)
		if _, ok := t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
			panic(req)
		}
		if !c.Request(e.Value.(request)) {
			return
		}
	}
	readaheadPieces := (me.Readahead + t.UsualPieceSize() - 1) / t.UsualPieceSize()
	for i := t.lastReadPiece; i < t.lastReadPiece+readaheadPieces && i < t.NumPieces(); i++ {
		for _, cs := range t.Pieces[i].shuffledPendingChunkSpecs() {
			if !c.Request(request{pp.Integer(i), cs}) {
				return
			}
		}
	}
	// Then finish off incomplete pieces in order of bytes remaining.
	for e := t.PiecesByBytesLeft.Front(); e != nil; e = e.Next() {
		index := e.Value.(int)
		// Stop when we're onto untouched pieces.
		if !t.PiecePartiallyDownloaded(index) {
			break
		}
		// Request chunks in random order to reduce overlap with other
		// connections.
		for _, cs := range t.Pieces[index].shuffledPendingChunkSpecs() {
			if !c.Request(request{pp.Integer(index), cs}) {
				return
			}
		}
	}
}
