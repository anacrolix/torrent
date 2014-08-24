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
	AssertNotRequested(*torrent, request)
}

type DefaultDownloadStrategy struct {
	heat map[*torrent]map[request]int
}

func (me *DefaultDownloadStrategy) AssertNotRequested(t *torrent, r request) {
	if me.heat[t][r] != 0 {
		panic("outstanding requests break invariant")
	}
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
		for e := t.IncompletePiecesByBytesLeft.Front(); e != nil; e = e.Next() {
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

func NewResponsiveDownloadStrategy(readahead int64) *responsiveDownloadStrategy {
	return &responsiveDownloadStrategy{
		Readahead:      readahead,
		lastReadOffset: make(map[*torrent]int64),
		priorities:     make(map[*torrent]map[request]struct{}),
		requestHeat:    make(map[*torrent]map[request]int),
	}
}

type responsiveDownloadStrategy struct {
	// How many bytes to preemptively download starting at the beginning of
	// the last piece read for a given torrent.
	Readahead      int64
	lastReadOffset map[*torrent]int64
	priorities     map[*torrent]map[request]struct{}
	requestHeat    map[*torrent]map[request]int
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
	me.requestHeat[t] = make(map[request]int)
}

func (me *responsiveDownloadStrategy) TorrentStopped(t *torrent) {
	delete(me.lastReadOffset, t)
	delete(me.priorities, t)
}
func (me *responsiveDownloadStrategy) DeleteRequest(t *torrent, r request) {
	rh := me.requestHeat[t]
	if rh[r] <= 0 {
		panic("request heat invariant broken")
	}
	rh[r]--
}

func (me *responsiveDownloadStrategy) FillRequests(t *torrent, c *connection) {
	th := me.requestHeat[t]
	requestWrapper := func(req request) bool {
		if c.RequestPending(req) {
			return true
		}
		again := c.Request(req)
		if c.RequestPending(req) {
			th[req]++
		}
		return again
	}

	for req := range me.priorities[t] {
		if _, ok := t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
			panic(req)
		}
		if !requestWrapper(req) {
			return
		}
	}

	if len(c.Requests) >= 16 {
		return
	}

	requestWrapper = func() func(request) bool {
		f := requestWrapper
		return func(req request) bool {
			if len(c.Requests) >= 32 {
				return false
			}
			return f(req)
		}
	}()

	if lastReadOffset, ok := me.lastReadOffset[t]; ok {
		var nextAhead int64
		for ahead := int64(0); ahead < me.Readahead; ahead = nextAhead {
			off := lastReadOffset + ahead
			req, ok := t.offsetRequest(off)
			if !ok {
				break
			}
			if !t.wantPiece(int(req.Index)) {
				nextAhead = ahead + int64(t.PieceLength(req.Index))
				continue
			}
			nextAhead = ahead + int64(req.Length)
			if !t.wantChunk(req) {
				continue
			}
			if th[req] >= func() int {
				// Determine allowed redundancy based on how far into the
				// readahead zone we're looking.
				if ahead >= (2*me.Readahead+2)/3 {
					return 1
				} else if ahead >= (me.Readahead+2)/3 {
					return 2
				} else {
					return 3
				}
			}() {
				continue
			}
			if !requestWrapper(req) {
				return
			}
		}
	}

	// t.assertIncompletePiecesByBytesLeftOrdering()
	for e := t.IncompletePiecesByBytesLeft.Front(); e != nil; e = e.Next() {
		p := e.Value.(int)
		if !t.PiecePartiallyDownloaded(p) && int(t.PieceLength(pp.Integer(p))) == t.UsualPieceSize() {
			break
		}
		for chunkSpec := range t.Pieces[p].PendingChunkSpecs {
			r := request{pp.Integer(p), chunkSpec}
			if th[r] >= 2 {
				continue
			}
			if !requestWrapper(r) {
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
	s.lastReadOffset[t] = off
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

func (s *responsiveDownloadStrategy) AssertNotRequested(t *torrent, r request) {
	if s.requestHeat[t][r] != 0 {
		panic("outstanding requests invariant broken")
	}
}
