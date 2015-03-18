package torrent

import (
	"container/heap"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bradfitz/iter"

	"bitbucket.org/anacrolix/go.torrent/data"
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"bitbucket.org/anacrolix/go.torrent/util"
	"github.com/anacrolix/libtorgo/bencode"
	"github.com/anacrolix/libtorgo/metainfo"
)

func (t *torrent) PieceNumPendingBytes(index int) (count pp.Integer) {
	if t.pieceComplete(index) {
		return 0
	}
	piece := t.Pieces[index]
	if !piece.EverHashed {
		return t.PieceLength(index)
	}
	pendingChunks := t.Pieces[index].PendingChunkSpecs
	count = pp.Integer(len(pendingChunks)) * chunkSize
	_lastChunkSpec := lastChunkSpec(t.PieceLength(index))
	if _lastChunkSpec.Length != chunkSize {
		if _, ok := pendingChunks[_lastChunkSpec]; ok {
			count += _lastChunkSpec.Length - chunkSize
		}
	}
	return
}

type peersKey struct {
	IPBytes string
	Port    int
}

// Data maintains per-piece persistent state.
type StatefulData interface {
	data.Data
	// We believe the piece data will pass a hash check.
	PieceCompleted(index int) error
	// Returns true if the piece is complete.
	PieceComplete(index int) bool
}

// Is not aware of Client. Maintains state of torrent for with-in a Client.
type torrent struct {
	stateMu sync.Mutex
	closing chan struct{}

	// Closed when no more network activity is desired. This includes
	// announcing, and communicating with peers.
	ceasingNetworking chan struct{}

	InfoHash InfoHash
	Pieces   []*piece
	// Total length of the torrent in bytes. Stored because it's not O(1) to
	// get this from the info dict.
	length int64

	data StatefulData

	// The info dict. Nil if we don't have it.
	Info *metainfo.Info
	// Active peer connections, running message stream loops.
	Conns []*connection
	// Set of addrs to which we're attempting to connect. Connections are
	// half-open until all handshakes are completed.
	HalfOpen map[string]struct{}

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known.
	Peers     map[peersKey]Peer
	wantPeers sync.Cond

	// BEP 12 Multitracker Metadata Extension. The tracker.Client instances
	// mirror their respective URLs from the announce-list metainfo key.
	Trackers [][]tracker.Client
	// Name used if the info name isn't available.
	DisplayName string
	// The bencoded bytes of the info dict.
	MetaData []byte
	// Each element corresponds to the 16KiB metadata pieces. If true, we have
	// received that piece.
	metadataHave []bool

	// Closed when .Info is set.
	gotMetainfo chan struct{}
	GotMetainfo <-chan struct{}

	pruneTimer *time.Timer
}

func (t *torrent) pieceComplete(piece int) bool {
	// TODO: This is called when setting metadata, and before storage is
	// assigned, which doesn't seem right.
	return t.data != nil && t.data.PieceComplete(piece)
}

// A file-like handle to torrent data that implements SectionOpener. Opened
// sections will be reused so long as Reads and ReadAt's are contiguous.
type handle struct {
	rc     io.ReadCloser
	rcOff  int64
	curOff int64
	so     SectionOpener
	size   int64
	t      Torrent
}

func (h *handle) Close() error {
	if h.rc != nil {
		return h.rc.Close()
	}
	return nil
}

func (h *handle) ReadAt(b []byte, off int64) (n int, err error) {
	return h.readAt(b, off)
}

func (h *handle) readAt(b []byte, off int64) (n int, err error) {
	avail := h.t.prepareRead(off)
	if int64(len(b)) > avail {
		b = b[:avail]
	}
	if int64(len(b)) > h.size-off {
		b = b[:h.size-off]
	}
	if h.rcOff != off && h.rc != nil {
		h.rc.Close()
		h.rc = nil
	}
	if h.rc == nil {
		h.rc, err = h.so.OpenSection(off, h.size-off)
		if err != nil {
			return
		}
		h.rcOff = off
	}
	n, err = h.rc.Read(b)
	h.rcOff += int64(n)
	return
}

func (h *handle) Read(b []byte) (n int, err error) {
	n, err = h.readAt(b, h.curOff)
	h.curOff = h.rcOff
	return
}

func (h *handle) Seek(off int64, whence int) (newOff int64, err error) {
	switch whence {
	case os.SEEK_SET:
		h.curOff = off
	case os.SEEK_CUR:
		h.curOff += off
	case os.SEEK_END:
		h.curOff = h.size + off
	default:
		err = errors.New("bad whence")
	}
	newOff = h.curOff
	return
}

// Implements Handle on top of an io.SectionReader.
type sectionReaderHandle struct {
	*io.SectionReader
}

func (sectionReaderHandle) Close() error { return nil }

func (T Torrent) NewReadHandle() Handle {
	if so, ok := T.data.(SectionOpener); ok {
		return &handle{
			so:   so,
			size: T.Length(),
			t:    T,
		}
	}
	return sectionReaderHandle{io.NewSectionReader(T, 0, T.Length())}
}

func (t *torrent) numConnsUnchoked() (num int) {
	for _, c := range t.Conns {
		if !c.PeerChoked {
			num++
		}
	}
	return
}

// There's a connection to that address already.
func (t *torrent) addrActive(addr string) bool {
	if _, ok := t.HalfOpen[addr]; ok {
		return true
	}
	for _, c := range t.Conns {
		if c.remoteAddr().String() == addr {
			return true
		}
	}
	return false
}

func (t *torrent) worstConnsHeap() (wcs *worstConns) {
	wcs = &worstConns{
		c: append([]*connection{}, t.Conns...),
		t: t,
	}
	heap.Init(wcs)
	return
}

func (t *torrent) ceaseNetworking() {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	select {
	case <-t.ceasingNetworking:
		return
	default:
	}
	close(t.ceasingNetworking)
	for _, c := range t.Conns {
		c.Close()
	}
	t.pruneTimer.Stop()
}

func (t *torrent) addPeer(p Peer) {
	t.Peers[peersKey{string(p.IP), p.Port}] = p
}

func (t *torrent) invalidateMetadata() {
	t.MetaData = nil
	t.metadataHave = nil
	t.Info = nil
}

func (t *torrent) SaveMetadataPiece(index int, data []byte) {
	if t.haveInfo() {
		return
	}
	if index >= len(t.metadataHave) {
		log.Printf("%s: ignoring metadata piece %d", t, index)
		return
	}
	copy(t.MetaData[(1<<14)*index:], data)
	t.metadataHave[index] = true
}

func (t *torrent) metadataPieceCount() int {
	return (len(t.MetaData) + (1 << 14) - 1) / (1 << 14)
}

func (t *torrent) haveMetadataPiece(piece int) bool {
	if t.haveInfo() {
		return (1<<14)*piece < len(t.MetaData)
	} else {
		return piece < len(t.metadataHave) && t.metadataHave[piece]
	}
}

func (t *torrent) metadataSizeKnown() bool {
	return t.MetaData != nil
}

func (t *torrent) metadataSize() int {
	return len(t.MetaData)
}

func infoPieceHashes(info *metainfo.Info) (ret []string) {
	for i := 0; i < len(info.Pieces); i += 20 {
		ret = append(ret, string(info.Pieces[i:i+20]))
	}
	return
}

// Called when metadata for a torrent becomes available.
func (t *torrent) setMetadata(md *metainfo.Info, infoBytes []byte, eventLocker sync.Locker) (err error) {
	t.Info = md
	t.length = 0
	for _, f := range t.Info.UpvertedFiles() {
		t.length += f.Length
	}
	t.MetaData = infoBytes
	t.metadataHave = nil
	for _, hash := range infoPieceHashes(md) {
		piece := &piece{}
		piece.Event.L = eventLocker
		util.CopyExact(piece.Hash[:], hash)
		t.Pieces = append(t.Pieces, piece)
	}
	for _, conn := range t.Conns {
		t.initRequestOrdering(conn)
		if err := conn.setNumPieces(t.numPieces()); err != nil {
			log.Printf("closing connection: %s", err)
			conn.Close()
		}
	}
	return
}

func (t *torrent) setStorage(td data.Data) (err error) {
	if c, ok := t.data.(io.Closer); ok {
		c.Close()
	}
	if sd, ok := td.(StatefulData); ok {
		t.data = sd
	} else {
		t.data = &statelessDataWrapper{td, make([]bool, t.Info.NumPieces())}
	}
	return
}

func (t *torrent) haveAllMetadataPieces() bool {
	if t.haveInfo() {
		return true
	}
	if t.metadataHave == nil {
		return false
	}
	for _, have := range t.metadataHave {
		if !have {
			return false
		}
	}
	return true
}

func (t *torrent) SetMetadataSize(bytes int64) {
	if t.MetaData != nil {
		return
	}
	if bytes > 10000000 { // 10MB, pulled from my ass.
		return
	}
	t.MetaData = make([]byte, bytes)
	t.metadataHave = make([]bool, (bytes+(1<<14)-1)/(1<<14))
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *torrent) Name() string {
	if t.haveInfo() {
		return t.Info.Name
	}
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return ""
}

func (t *torrent) pieceStatusChar(index int) byte {
	p := t.Pieces[index]
	switch {
	case t.pieceComplete(index):
		return 'C'
	case p.QueuedForHash:
		return 'Q'
	case p.Hashing:
		return 'H'
	case !p.EverHashed:
		return '?'
	case t.piecePartiallyDownloaded(index):
		switch p.Priority {
		case piecePriorityNone:
			return 'F' // Forgotten
		default:
			return 'P'
		}
	default:
		switch p.Priority {
		case piecePriorityNone:
			return 'z'
		case piecePriorityNow:
			return '!'
		case piecePriorityReadahead:
			return 'R'
		case piecePriorityNext:
			return 'N'
		default:
			return '.'
		}
	}
}

func (t *torrent) metadataPieceSize(piece int) int {
	return metadataPieceSize(len(t.MetaData), piece)
}

func (t *torrent) newMetadataExtensionMessage(c *connection, msgType int, piece int, data []byte) pp.Message {
	d := map[string]int{
		"msg_type": msgType,
		"piece":    piece,
	}
	if data != nil {
		d["total_size"] = len(t.MetaData)
	}
	p, err := bencode.Marshal(d)
	if err != nil {
		panic(err)
	}
	return pp.Message{
		Type:            pp.Extended,
		ExtendedID:      byte(c.PeerExtensionIDs["ut_metadata"]),
		ExtendedPayload: append(p, data...),
	}
}

type PieceStatusCharSequence struct {
	Char  byte
	Count int
}

func (t *torrent) PieceStatusCharSequences() []PieceStatusCharSequence {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.pieceStatusCharSequences()
}

// Returns the length of sequences of identical piece status chars.
func (t *torrent) pieceStatusCharSequences() (ret []PieceStatusCharSequence) {
	var (
		char  byte
		count int
	)
	writeSequence := func() {
		ret = append(ret, PieceStatusCharSequence{char, count})
	}
	if len(t.Pieces) != 0 {
		char = t.pieceStatusChar(0)
	}
	for index := range t.Pieces {
		char1 := t.pieceStatusChar(index)
		if char1 == char {
			count++
		} else {
			writeSequence()
			char = char1
			count = 1
		}
	}
	if count != 0 {
		writeSequence()
	}
	return
}

func (t *torrent) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "Infohash: %x\n", t.InfoHash)
	fmt.Fprintf(w, "Piece length: %s\n", func() string {
		if t.haveInfo() {
			return fmt.Sprint(t.usualPieceSize())
		} else {
			return "?"
		}
	}())
	if t.haveInfo() {
		fmt.Fprint(w, "Pieces: ")
		for _, seq := range t.pieceStatusCharSequences() {
			fmt.Fprintf(w, "%d%c ", seq.Count, seq.Char)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Trackers: ")
	for _, tier := range t.Trackers {
		for _, tr := range tier {
			fmt.Fprintf(w, "%q ", tr.String())
		}
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "Pending peers: %d\n", len(t.Peers))
	fmt.Fprintf(w, "Half open: %d\n", len(t.HalfOpen))
	fmt.Fprintf(w, "Active peers: %d\n", len(t.Conns))
	sort.Sort(&worstConns{
		c: t.Conns,
		t: t,
	})
	for _, c := range t.Conns {
		c.WriteStatus(w, t)
	}
}

func (t *torrent) String() string {
	s := t.Name()
	if s == "" {
		s = fmt.Sprintf("%x", t.InfoHash)
	}
	return s
}

func (t *torrent) haveInfo() bool {
	return t.Info != nil
}

// TODO: Include URIs that weren't converted to tracker clients.
func (t *torrent) announceList() (al [][]string) {
	for _, tier := range t.Trackers {
		var l []string
		for _, tr := range tier {
			l = append(l, tr.URL())
		}
		al = append(al, l)
	}
	return
}

func (t *torrent) MetaInfo() *metainfo.MetaInfo {
	if t.MetaData == nil {
		panic("info bytes not set")
	}
	return &metainfo.MetaInfo{
		Info: metainfo.InfoEx{
			Info:  *t.Info,
			Bytes: t.MetaData,
		},
		CreationDate: time.Now().Unix(),
		Comment:      "dynamic metainfo from client",
		CreatedBy:    "go.torrent",
		AnnounceList: t.announceList(),
	}
}

func (t *torrent) bytesLeft() (left int64) {
	if !t.haveInfo() {
		return -1
	}
	for i := 0; i < t.numPieces(); i++ {
		left += int64(t.PieceNumPendingBytes(i))
	}
	return
}

func (t *torrent) piecePartiallyDownloaded(index int) bool {
	return t.PieceNumPendingBytes(index) != t.PieceLength(index)
}

func numChunksForPiece(chunkSize int, pieceSize int) int {
	return (pieceSize + chunkSize - 1) / chunkSize
}

func (t *torrent) usualPieceSize() int {
	return int(t.Info.PieceLength)
}

func (t *torrent) lastPieceSize() int {
	return int(t.PieceLength(t.numPieces() - 1))
}

func (t *torrent) numPieces() int {
	return t.Info.NumPieces()
}

func (t *torrent) numPiecesCompleted() (num int) {
	for i := range iter.N(t.Info.NumPieces()) {
		if t.pieceComplete(i) {
			num++
		}
	}
	return
}

func (t *torrent) Length() int64 {
	return t.length
}

func (t *torrent) isClosed() bool {
	select {
	case <-t.closing:
		return true
	default:
		return false
	}
}

func (t *torrent) close() (err error) {
	if t.isClosed() {
		return
	}
	t.ceaseNetworking()
	close(t.closing)
	if c, ok := t.data.(io.Closer); ok {
		c.Close()
	}
	for _, conn := range t.Conns {
		conn.Close()
	}
	return
}

// Return the request that would include the given offset into the torrent data.
func torrentOffsetRequest(torrentLength, pieceSize, chunkSize, offset int64) (
	r request, ok bool) {
	if offset < 0 || offset >= torrentLength {
		return
	}
	r.Index = pp.Integer(offset / pieceSize)
	r.Begin = pp.Integer(offset % pieceSize / chunkSize * chunkSize)
	left := torrentLength - int64(r.Index)*pieceSize - int64(r.Begin)
	if chunkSize < left {
		r.Length = pp.Integer(chunkSize)
	} else {
		r.Length = pp.Integer(left)
	}
	ok = true
	return
}

func torrentRequestOffset(torrentLength, pieceSize int64, r request) (off int64) {
	off = int64(r.Index)*pieceSize + int64(r.Begin)
	if off < 0 || off >= torrentLength {
		panic("invalid request")
	}
	return
}

func (t *torrent) requestOffset(r request) int64 {
	return torrentRequestOffset(t.Length(), int64(t.usualPieceSize()), r)
}

// Return the request that would include the given offset into the torrent data.
func (t *torrent) offsetRequest(off int64) (req request, ok bool) {
	return torrentOffsetRequest(t.Length(), t.Info.PieceLength, chunkSize, off)
}

func (t *torrent) writeChunk(piece int, begin int64, data []byte) (err error) {
	_, err = t.data.WriteAt(data, int64(piece)*t.Info.PieceLength+begin)
	return
}

func (t *torrent) bitfield() (bf []bool) {
	for _, p := range t.Pieces {
		bf = append(bf, p.EverHashed && len(p.PendingChunkSpecs) == 0)
	}
	return
}

func (t *torrent) pieceChunks(piece int) (css []chunkSpec) {
	css = make([]chunkSpec, 0, (t.PieceLength(piece)+chunkSize-1)/chunkSize)
	var cs chunkSpec
	for left := t.PieceLength(piece); left != 0; left -= cs.Length {
		cs.Length = left
		if cs.Length > chunkSize {
			cs.Length = chunkSize
		}
		css = append(css, cs)
		cs.Begin += cs.Length
	}
	return
}

func (t *torrent) pendAllChunkSpecs(index int) {
	piece := t.Pieces[index]
	if piece.PendingChunkSpecs == nil {
		piece.PendingChunkSpecs = make(
			map[chunkSpec]struct{},
			(t.PieceLength(index)+chunkSize-1)/chunkSize)
	}
	pcss := piece.PendingChunkSpecs
	for _, cs := range t.pieceChunks(int(index)) {
		pcss[cs] = struct{}{}
	}
	return
}

type Peer struct {
	Id     [20]byte
	IP     net.IP
	Port   int
	Source peerSource
	// Peer is known to support encryption.
	SupportsEncryption bool
}

func (t *torrent) PieceLength(piece int) (len_ pp.Integer) {
	if int(piece) == t.numPieces()-1 {
		len_ = pp.Integer(t.Length() % t.Info.PieceLength)
	}
	if len_ == 0 {
		len_ = pp.Integer(t.Info.PieceLength)
	}
	return
}

func (t *torrent) hashPiece(piece pp.Integer) (ps pieceSum) {
	hash := pieceHash.New()
	t.data.WriteSectionTo(hash, int64(piece)*t.Info.PieceLength, t.Info.PieceLength)
	util.CopyExact(ps[:], hash.Sum(nil))
	return
}

func (t *torrent) haveAllPieces() bool {
	if !t.haveInfo() {
		return false
	}
	for i := range t.Pieces {
		if !t.pieceComplete(i) {
			return false
		}
	}
	return true
}

func (me *torrent) haveAnyPieces() bool {
	for i := range me.Pieces {
		if me.pieceComplete(i) {
			return true
		}
	}
	return false
}

func (t *torrent) havePiece(index int) bool {
	return t.haveInfo() && t.pieceComplete(index)
}

func (t *torrent) haveChunk(r request) bool {
	p := t.Pieces[r.Index]
	if !p.EverHashed {
		return false
	}
	_, ok := p.PendingChunkSpecs[r.chunkSpec]
	return !ok
}

func (t *torrent) wantChunk(r request) bool {
	if !t.wantPiece(int(r.Index)) {
		return false
	}
	_, ok := t.Pieces[r.Index].PendingChunkSpecs[r.chunkSpec]
	return ok
}

func (t *torrent) wantPiece(index int) bool {
	if !t.haveInfo() {
		return false
	}
	p := t.Pieces[index]
	// Put piece complete check last, since it's the slowest!
	return p.Priority != piecePriorityNone && !p.QueuedForHash && !p.Hashing && !t.pieceComplete(index)
}

func (t *torrent) connHasWantedPieces(c *connection) bool {
	return c.pieceRequestOrder != nil && c.pieceRequestOrder.First() != nil
}

func (t *torrent) extentPieces(off, _len int64) (pieces []int) {
	for i := off / int64(t.usualPieceSize()); i*int64(t.usualPieceSize()) < off+_len; i++ {
		pieces = append(pieces, int(i))
	}
	return
}
