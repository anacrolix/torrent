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
	// First request prioritized chunks.
	// for e := t.Priorities.Front(); e != nil; e = e.Next() {
	// 	if !addRequest(e.Value.(request)) {
	// 		return
	// 	}
	// }
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
		priorities:    make(map[*torrent]*list.List),
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
	me.priorities[t] = list.New()
}

func (me *responsiveDownloadStrategy) TorrentStopped(t *torrent) {
	delete(me.lastReadPiece, t)
	delete(me.priorities, t)
}
func (responsiveDownloadStrategy) DeleteRequest(*torrent, request) {}

func (me *responsiveDownloadStrategy) FillRequests(t *torrent, c *connection) {
	if len(c.Requests) >= (c.PeerMaxRequests+1)/2 || len(c.Requests) >= 64 {
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

	prios := me.priorities[t]
	for e := prios.Front(); e != nil; e = e.Next() {
		req := e.Value.(request)
		if _, ok := t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
			panic(req)
		}
		if !requestWrapper(e.Value.(request)) {
			return
		}
	}

	if lastReadPiece, ok := me.lastReadPiece[t]; ok {
		readaheadPieces := (me.Readahead + t.UsualPieceSize() - 1) / t.UsualPieceSize()
		for i := lastReadPiece; i < lastReadPiece+readaheadPieces && i < t.NumPieces(); i++ {
			for _, cs := range t.Pieces[i].shuffledPendingChunkSpecs() {
				if !requestWrapper(request{pp.Integer(i), cs}) {
					return
				}
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
	prios := me.priorities[t]
	var next *list.Element
	for e := prios.Front(); e != nil; e = next {
		next = e.Next()
		if e.Value.(request) == req {
			prios.Remove(e)
		}
	}
}

func (me *responsiveDownloadStrategy) TorrentGotPiece(t *torrent, piece int) {
	var next *list.Element
	prios := me.priorities[t]
	for e := prios.Front(); e != nil; e = next {
		next = e.Next()
		if int(e.Value.(request).Index) == piece {
			prios.Remove(e)
		}
	}
}

func (s *responsiveDownloadStrategy) TorrentPrioritize(t *torrent, off, _len int64) {
	newPriorities := make([]request, 0, (_len+chunkSize-1)/chunkSize)
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
		if !t.wantPiece(int(req.Index)) {
			continue
		}
		newPriorities = append(newPriorities, req)
	}
	if len(newPriorities) == 0 {
		return
	}
	s.lastReadPiece[t] = int(newPriorities[0].Index)
	if t.wantChunk(newPriorities[0]) {
		s.priorities[t].PushFront(newPriorities[0])
	}
	for _, req := range newPriorities[1:] {
		if t.wantChunk(req) {
			s.priorities[t].PushBack(req)
		}
	}
}
