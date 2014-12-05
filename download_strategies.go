package torrent

import (
	"container/heap"
	"fmt"
	"io"
	"math/rand"

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

func NewResponsiveDownloadStrategy(readahead int64) *responsiveDownloadStrategy {
	return &responsiveDownloadStrategy{
		Readahead:      readahead,
		lastReadOffset: make(map[*torrent]int64),
		priorities:     make(map[*torrent]map[request]struct{}),
		requestHeat:    make(map[*torrent]map[request]int),
		rand:           rand.New(rand.NewSource(1337)),
	}
}

type responsiveDownloadStrategy struct {
	// How many bytes to preemptively download starting at the beginning of
	// the last piece read for a given torrent.
	Readahead      int64
	lastReadOffset map[*torrent]int64
	priorities     map[*torrent]map[request]struct{}
	requestHeat    map[*torrent]map[request]int
	rand           *rand.Rand // Avoid global lock
	dummyConn      *connection
}

func (me *responsiveDownloadStrategy) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "Priorities:\n")
	for t, pp := range me.priorities {
		fmt.Fprintf(w, "\t%s:", t.Name())
		for r := range pp {
			fmt.Fprintf(w, " %v", r)
		}
		fmt.Fprintln(w)
	}
}

func (me *responsiveDownloadStrategy) TorrentStarted(t *torrent) {
	me.priorities[t] = make(map[request]struct{})
	me.requestHeat[t] = make(map[request]int)
	me.dummyConn = &connection{}
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

type requestFiller struct {
	c *connection
	t *torrent
	s *responsiveDownloadStrategy
}

// Wrapper around connection.request that tracks request heat.
func (me *requestFiller) request(req request) bool {
	if me.c.RequestPending(req) {
		return true
	}
	if !me.t.wantChunk(req) {
		return true
	}
	again := me.c.Request(req)
	if me.c.RequestPending(req) {
		me.s.requestHeat[me.t][req]++
	}
	return again
}

// Adds additional constraints around the request heat wrapper.
func (me *requestFiller) conservativelyRequest(req request) bool {
	again := me.request(req)
	if len(me.c.Requests) >= 50 {
		return false
	}
	return again
}

// Fill priority requests.
func (me *requestFiller) priorities() bool {
	for req := range me.s.priorities[me.t] {
		// TODO: Perhaps this filter should be applied to every request?
		if _, ok := me.t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
			panic(req)
		}
		if !me.request(req) {
			return false
		}
	}
	return true
}

// Fill requests, with all contextual information available in the receiver.
func (me *requestFiller) Run() {
	if !me.priorities() {
		return
	}
	if len(me.c.Requests) > 25 {
		return
	}
	if !me.readahead() {
		return
	}
	if len(me.c.Requests) > 0 {
		return
	}
	me.completePartial()
}

// Request partial pieces that aren't in the readahead zone.
func (me *requestFiller) completePartial() bool {
	t := me.t
	th := me.s.requestHeat[t]
	lro, lroOk := me.s.lastReadOffset[t]
	for e := t.IncompletePiecesByBytesLeft.Front(); e != nil; e = e.Next() {
		p := e.Value.(int)
		// Stop when we reach pieces that aren't partial and aren't smaller
		// than usual.
		if !t.PiecePartiallyDownloaded(p) && int(t.PieceLength(pp.Integer(p))) == t.UsualPieceSize() {
			break
		}
		// Skip pieces that are entirely inside the readahead zone.
		if lroOk {
			pieceOff := int64(p) * int64(t.UsualPieceSize())
			pieceEndOff := pieceOff + int64(t.PieceLength(pp.Integer(p)))
			if pieceOff >= lro && pieceEndOff < lro+me.s.Readahead {
				continue
			}
		}
		for chunkSpec := range t.Pieces[p].PendingChunkSpecs {
			r := request{pp.Integer(p), chunkSpec}
			if th[r] >= 1 {
				continue
			}
			if lroOk {
				off := me.t.requestOffset(r)
				if off >= lro && off < lro+me.s.Readahead {
					continue
				}
			}
			if !me.conservativelyRequest(r) {
				return false
			}
		}
	}
	return true
}

// Returns all wanted chunk specs in the readahead zone.
func (me *requestFiller) pendingReadaheadChunks() (ret []request) {
	t := me.t
	lastReadOffset, ok := me.s.lastReadOffset[t]
	if !ok {
		return
	}
	ret = make([]request, 0, (me.s.Readahead+chunkSize-1)/chunkSize)
	for pi := int(lastReadOffset / int64(t.UsualPieceSize())); pi < t.NumPieces() && int64(pi)*int64(t.UsualPieceSize()) < lastReadOffset+me.s.Readahead; pi++ {
		if t.havePiece(pi) || !me.c.PeerHasPiece(pp.Integer(pi)) {
			continue
		}
		for cs := range t.Pieces[pi].PendingChunkSpecs {
			r := request{pp.Integer(pi), cs}
			if _, ok := me.c.Requests[r]; ok {
				continue
			}
			if off := t.requestOffset(r); off < lastReadOffset || off >= lastReadOffset+me.s.Readahead {
				continue
			}
			ret = append(ret, r)
		}
	}
	return
}

// Min-heap of int.
type intHeap []int

func (h intHeap) Len() int            { return len(h) }
func (h intHeap) Less(i, j int) bool  { return h[i] < h[j] }
func (h intHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *intHeap) Push(x interface{}) { *h = append(*h, x.(int)) }
func (h *intHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func (me *requestFiller) readahead() bool {
	rr := me.pendingReadaheadChunks()
	if len(rr) == 0 {
		return true
	}
	// Produce a partially sorted random permutation into the readahead chunks
	// to somewhat preserve order but reducing wasted chunks due to overlap
	// with other peers.
	ii := new(intHeap)
	*ii = me.s.rand.Perm(len(rr))
	heap.Init(ii)
	for _, i := range *ii {
		if !me.conservativelyRequest(rr[i]) {
			return false
		}
	}
	return true
}

func (me *responsiveDownloadStrategy) FillRequests(t *torrent, c *connection) {
	rf := requestFiller{c: c, t: t, s: me}
	rf.Run()
	return
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
		if !t.haveChunk(req) {
			s.priorities[t][req] = struct{}{}
		}
	}
}

func (s *responsiveDownloadStrategy) AssertNotRequested(t *torrent, r request) {
	if s.requestHeat[t][r] != 0 {
		panic("outstanding requests invariant broken")
	}
}

func (me *responsiveDownloadStrategy) PendingData(t *torrent) bool {
	if len(me.priorities[t]) != 0 {
		return true
	}
	for index := range t.Pieces {
		if t.wantPiece(index) {
			return true
		}
	}
	return false
}
