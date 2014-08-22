package torrent

import (
	"fmt"
	"io"

	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

type DownloadStrategy interface {
	FillRequests(t *torrent, c *connection)
	TorrentStarted(t *torrent)
	TorrentStopped(t *torrent)
	DeleteRequest(t *torrent, r request)
	TorrentPrioritize(t *torrent, off, _len int64)
	TorrentGotChunk(t *torrent, r request)
	TorrentGotPiece(t *torrent, piece int)
	WriteStatus(w io.Writer)
}

type DefaultDownloadStrategy struct {
	heat map[*torrent]map[request]int
}

func (me *DefaultDownloadStrategy) WriteStatus(w io.Writer) {}

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

func (me *DefaultDownloadStrategy) TorrentGotChunk(t *torrent, c request)      {}
func (me *DefaultDownloadStrategy) TorrentGotPiece(t *torrent, piece int)      {}
func (*DefaultDownloadStrategy) TorrentPrioritize(t *torrent, off, _len int64) {}

func NewResponsiveDownloadStrategy(readahead int) *responsiveDownloadStrategy {
	return &responsiveDownloadStrategy{
		Readahead:     readahead,
		lastReadPiece: make(map[*torrent]int),
		priorities:    make(map[*torrent]map[request]struct{}),
	}
}

type responsiveDownloadStrategy struct {
	// How many bytes to preemptively download starting at the beginning of
	// the last piece read for a given torrent.
	Readahead     int
	lastReadPiece map[*torrent]int
	priorities    map[*torrent]map[request]struct{}
}

func (me *responsiveDownloadStrategy) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "Priorities:\n")
	for t, pp := range me.priorities {
		fmt.Fprintf(w, "\t%s:", t.Name())
		for r := range pp {
			fmt.Fprintf(w, "%v ", r)
		}
		fmt.Fprintln(w)
	}
}

func (me *responsiveDownloadStrategy) TorrentStarted(t *torrent) {
	me.priorities[t] = make(map[request]struct{})
}

func (me *responsiveDownloadStrategy) TorrentStopped(t *torrent) {
	delete(me.lastReadPiece, t)
	delete(me.priorities, t)
}
func (responsiveDownloadStrategy) DeleteRequest(*torrent, request) {}

func (me *responsiveDownloadStrategy) FillRequests(t *torrent, c *connection) {
	for req := range me.priorities[t] {
		if _, ok := t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
			panic(req)
		}
		if !c.Request(req) {
			return
		}
	}

	if len(c.Requests) >= 32 {
		return
	}

	// Short circuit request fills at a level that might reduce receiving of
	// unnecessary chunks.
	requestWrapper := func(r request) bool {
		if len(c.Requests) >= 64 {
			return false
		}
		return c.Request(r)
	}

	requestPiece := func(piece int) bool {
		if piece >= t.NumPieces() {
			return true
		}
		for _, cs := range t.Pieces[piece].shuffledPendingChunkSpecs() {
			if !requestWrapper(request{pp.Integer(piece), cs}) {
				return false
			}
		}
		return true
	}

	if lastReadPiece, ok := me.lastReadPiece[t]; ok {
		readaheadPieces := (me.Readahead + t.UsualPieceSize() - 1) / t.UsualPieceSize()
		for i := 0; i < readaheadPieces; i++ {
			if !requestPiece(lastReadPiece + i) {
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
			if !requestWrapper(request{pp.Integer(index), cs}) {
				return
			}
		}
	}
}

func (me *responsiveDownloadStrategy) TorrentGotChunk(t *torrent, req request) {
	delete(me.priorities[t], req)
}

func (me *responsiveDownloadStrategy) TorrentGotPiece(t *torrent, piece int) {
	for _, cs := range t.pieceChunks(piece) {
		delete(me.priorities[t], request{pp.Integer(piece), cs})
	}
}

func (s *responsiveDownloadStrategy) TorrentPrioritize(t *torrent, off, _len int64) {
	s.lastReadPiece[t] = int(off / int64(t.UsualPieceSize()))
	for _len > 0 {
		req, ok := t.offsetRequest(off)
		if !ok {
			panic("bad offset")
		}
		reqOff := t.requestOffset(req)
		// Gain the alignment adjustment.
		_len += off - reqOff
		// Lose the length of this block.
		_len -= int64(req.Length)
		off = reqOff + int64(req.Length)
		if t.wantChunk(req) {
			s.priorities[t][req] = struct{}{}
		}
	}
}
