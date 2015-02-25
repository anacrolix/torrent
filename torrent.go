package torrent

import (
	"container/heap"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"bitbucket.org/anacrolix/go.torrent/util"
	"github.com/anacrolix/libtorgo/bencode"
	"github.com/anacrolix/libtorgo/metainfo"
)

func (t *torrent) PieceNumPendingBytes(index pp.Integer) (count pp.Integer) {
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

type TorrentData interface {
	ReadAt(p []byte, off int64) (n int, err error)
	Close()
	WriteAt(p []byte, off int64) (n int, err error)
	WriteSectionTo(w io.Writer, off, n int64) (written int64, err error)
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
	length   int64

	data TorrentData

	Info *MetaInfo
	// Active peer connections.
	Conns []*connection
	// Set of addrs to which we're attempting to connect.
	HalfOpen map[string]struct{}

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known.
	Peers     map[peersKey]Peer
	wantPeers sync.Cond

	// BEP 12 Multitracker Metadata Extension. The tracker.Client instances
	// mirror their respective URLs from the announce-list metainfo key.
	Trackers     [][]tracker.Client
	DisplayName  string
	MetaData     []byte
	metadataHave []bool

	gotMetainfo chan struct{}
	GotMetainfo <-chan struct{}

	pruneTimer *time.Timer
}

func (t *torrent) numConnsUnchoked() (num int) {
	for _, c := range t.Conns {
		if !c.PeerChoked {
			num++
		}
	}
	return
}

func (t *torrent) addrActive(addr string) bool {
	if _, ok := t.HalfOpen[addr]; ok {
		return true
	}
	for _, c := range t.Conns {
		if c.Socket.RemoteAddr().String() == addr {
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

func (t *torrent) AddPeers(pp []Peer) {
	for _, p := range pp {
		t.Peers[peersKey{string(p.IP), p.Port}] = p
	}
}

func (t *torrent) InvalidateMetadata() {
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

func (t *torrent) MetadataPieceCount() int {
	return (len(t.MetaData) + (1 << 14) - 1) / (1 << 14)
}

func (t *torrent) HaveMetadataPiece(piece int) bool {
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
func (t *torrent) setMetadata(md metainfo.Info, infoBytes []byte, eventLocker sync.Locker) (err error) {
	t.Info = newMetaInfo(&md)
	t.length = 0
	for _, f := range t.Info.UpvertedFiles() {
		t.length += f.Length
	}
	t.MetaData = infoBytes
	t.metadataHave = nil
	for _, hash := range infoPieceHashes(&md) {
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

func (t *torrent) setStorage(td TorrentData) (err error) {
	if t.data != nil {
		t.data.Close()
	}
	t.data = td
	return
}

func (t *torrent) HaveAllMetadataPieces() bool {
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

func (t *torrent) Name() string {
	if t.haveInfo() {
		return t.Info.Name
	}
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return t.InfoHash.HexString()
}

func (t *torrent) pieceStatusChar(index int) byte {
	p := t.Pieces[index]
	switch {
	case p.Complete():
		return 'C'
	case p.QueuedForHash:
		return 'Q'
	case p.Hashing:
		return 'H'
	case !p.EverHashed:
		return '?'
	case t.PiecePartiallyDownloaded(index):
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

func (t *torrent) NewMetadataExtensionMessage(c *connection, msgType int, piece int, data []byte) pp.Message {
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
			return fmt.Sprint(t.UsualPieceSize())
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
		c.WriteStatus(w)
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
func (t *torrent) AnnounceList() (al [][]string) {
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
			Info:  *t.Info.Info,
			Bytes: t.MetaData,
		},
		CreationDate: time.Now().Unix(),
		Comment:      "dynamic metainfo from client",
		CreatedBy:    "go.torrent",
		AnnounceList: t.AnnounceList(),
	}
}

func (t *torrent) BytesLeft() (left int64) {
	if !t.haveInfo() {
		return -1
	}
	for i := pp.Integer(0); i < pp.Integer(t.numPieces()); i++ {
		left += int64(t.PieceNumPendingBytes(i))
	}
	return
}

func (t *torrent) PiecePartiallyDownloaded(index int) bool {
	return t.PieceNumPendingBytes(pp.Integer(index)) != t.PieceLength(pp.Integer(index))
}

func NumChunksForPiece(chunkSize int, pieceSize int) int {
	return (pieceSize + chunkSize - 1) / chunkSize
}

func (t *torrent) UsualPieceSize() int {
	return int(t.Info.PieceLength)
}

func (t *torrent) LastPieceSize() int {
	return int(t.PieceLength(pp.Integer(t.numPieces() - 1)))
}

func (t *torrent) numPieces() int {
	return len(t.Info.Pieces) / 20
}

func (t *torrent) NumPiecesCompleted() (num int) {
	for _, p := range t.Pieces {
		if p.Complete() {
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
	if t.data != nil {
		t.data.Close()
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
	return torrentRequestOffset(t.Length(), int64(t.UsualPieceSize()), r)
}

// Return the request that would include the given offset into the torrent data.
func (t *torrent) offsetRequest(off int64) (req request, ok bool) {
	return torrentOffsetRequest(t.Length(), t.Info.PieceLength, chunkSize, off)
}

func (t *torrent) WriteChunk(piece int, begin int64, data []byte) (err error) {
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
	css = make([]chunkSpec, 0, (t.PieceLength(pp.Integer(piece))+chunkSize-1)/chunkSize)
	var cs chunkSpec
	for left := t.PieceLength(pp.Integer(piece)); left != 0; left -= cs.Length {
		cs.Length = left
		if cs.Length > chunkSize {
			cs.Length = chunkSize
		}
		css = append(css, cs)
		cs.Begin += cs.Length
	}
	return
}

func (t *torrent) pendAllChunkSpecs(index pp.Integer) {
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
}

func (t *torrent) PieceLength(piece pp.Integer) (len_ pp.Integer) {
	if int(piece) == t.numPieces()-1 {
		len_ = pp.Integer(t.Length() % t.Info.PieceLength)
	}
	if len_ == 0 {
		len_ = pp.Integer(t.Info.PieceLength)
	}
	return
}

func (t *torrent) HashPiece(piece pp.Integer) (ps pieceSum) {
	hash := pieceHash.New()
	t.data.WriteSectionTo(hash, int64(piece)*t.Info.PieceLength, t.Info.PieceLength)
	util.CopyExact(ps[:], hash.Sum(nil))
	return
}
func (t *torrent) haveAllPieces() bool {
	if !t.haveInfo() {
		return false
	}
	for _, piece := range t.Pieces {
		if !piece.Complete() {
			return false
		}
	}
	return true
}

func (me *torrent) haveAnyPieces() bool {
	for _, piece := range me.Pieces {
		if piece.Complete() {
			return true
		}
	}
	return false
}

func (t *torrent) havePiece(index int) bool {
	return t.haveInfo() && t.Pieces[index].Complete()
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
	return p.EverHashed && len(p.PendingChunkSpecs) != 0 && p.Priority != piecePriorityNone
}

func (t *torrent) connHasWantedPieces(c *connection) bool {
	for p := range t.Pieces {
		if t.wantPiece(p) && c.PeerHasPiece(pp.Integer(p)) {
			return true
		}
	}
	return false
}

func (t *torrent) extentPieces(off, _len int64) (pieces []int) {
	for i := off / int64(t.UsualPieceSize()); i*int64(t.UsualPieceSize()) < off+_len; i++ {
		pieces = append(pieces, int(i))
	}
	return
}
