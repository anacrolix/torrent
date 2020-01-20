package torrent

import (
	"container/heap"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	l2 "log"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unsafe"

	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/bitmap"
	"github.com/anacrolix/missinggo/perf"
	"github.com/anacrolix/missinggo/pubsub"
	"github.com/anacrolix/missinggo/slices"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/tracker"
)

// Tuner runtime tuning of an actively running torrent.
type Tuner func(*torrent)

// TuneMaxConnections adjust the maximum connections allowed for a torrent.
func TuneMaxConnections(m int) Tuner {
	return func(t *torrent) {
		t.SetMaxEstablishedConns(m)
	}
}

// Torrent represents the state of a torrent within a client.
// interface is currently being used to ease the transition of to a cleaner API.
// Many methods should not be called before the info is available,
// see .Info and .GotInfo.
type Torrent interface {
	Metadata() Metadata
	Tune(...Tuner) error
	Name() string                // TODO: remove, should be pulled from Torrent()
	Metainfo() metainfo.MetaInfo // TODO: remove, should be pulled from Torrent()
	BytesCompleted() int64       // TODO: maybe should be pulled from torrent, it has a reference to the storage implementation.
	VerifyData()                 // TODO: maybe should be pulled from torrent, it has a reference to the storage implementation.
	AddClientPeer(cl *Client)    // TODO: remove, this should only be used in tests.
	NewReader() Reader           // TODO: maybe should be pulled from torrent, it has a reference to the storage implementation.
	Seeding() bool               //
	Stats() TorrentStats         //
	Info() *metainfo.Info        // TODO: remove, this should be pulled from Torrent()
	GotInfo() <-chan struct{}    // TODO: remove, torrents should never be returned in they don't have the meta info.
	DownloadAll()                // TODO: rethink this. does it even need to exist or can it be rolled up into Start/Download.
	Files() []*File              // TODO: maybe should be pulled from torrent, it has a reference to the storage implementation.
	AddPeers(pp []Peer)
	SubscribePieceStateChanges() *pubsub.Subscription
	PieceStateRuns() []PieceStateRun
}

// Maintains state of torrent within a Client.
type torrent struct {
	// Torrent-level aggregate statistics. First in struct to ensure 64-bit
	// alignment. See #262.
	stats ConnStats

	lcount uint64
	ucount uint64

	cln    *Client
	config *ClientConfig
	_mu    *sync.RWMutex
	logger log.Logger

	networkingEnabled bool

	// How long to avoid duplicating a pending request.
	duplicateRequestTimeout time.Duration

	closed   missinggo.Event
	infoHash metainfo.Hash
	pieces   []Piece
	// Values are the piece indices that changed.
	pieceStateChanges *pubsub.PubSub
	// The size of chunks to request from peers over the wire. This is
	// normally 16KiB by convention these days.
	chunkSize pp.Integer
	chunkPool *sync.Pool

	// The storage to open when the info dict becomes available.
	storageOpener *storage.Client
	// Storage for torrent data.
	storage *storage.Torrent
	// Read-locked for using storage, and write-locked for Closing.
	storageLock sync.RWMutex

	// TODO: Only announce stuff is used?
	metainfo metainfo.MetaInfo

	// The info dict. nil if we don't have it (yet).
	info  *metainfo.Info
	files *[]*File

	// Active peer connections, running message stream loops. TODO: Make this
	// open (not-closed) connections only.
	conns map[*connection]struct{}

	maxEstablishedConns int

	// Set of addrs to which we're attempting to connect. Connections are
	// half-open until all handshakes are completed.
	halfOpen    map[string]Peer
	fastestConn *connection

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known in
	// the swarm.
	peers          prioritizedPeers
	wantPeersEvent missinggo.Event

	// An announcer for each tracker URL.
	trackerAnnouncers map[string]*trackerScraper
	// How many times we've initiated a DHT announce. TODO: Move into stats.
	numDHTAnnounces int

	// Name used if the info name isn't available. Should be cleared when the
	// Info does become available.
	nameMu      sync.RWMutex
	displayName string

	// The bencoded bytes of the info dict. This is actively manipulated if
	// the info bytes aren't initially available, and we try to fetch them
	// from peers.
	metadataBytes []byte
	// Each element corresponds to the 16KiB metadata pieces. If true, we have
	// received that piece.
	metadataCompletedChunks []bool
	metadataChanged         sync.Cond

	// Set when .Info is obtained.
	gotMetainfo missinggo.Event

	readers               map[*reader]struct{}
	readerNowPieces       bitmap.Bitmap
	readerReadaheadPieces bitmap.Bitmap

	// chunks management tracks the current status of the different chunks
	piecesM *chunks

	// digest management determines if pieces are valid.
	digests digests

	// A pool of piece priorities []int for assignment to new connections.
	// These "inclinations" are used to give connections preference for
	// different pieces.
	connPieceInclinationPool sync.Pool

	// signal events on this torrent.
	event *sync.Cond
}

// Metadata provides enough information to lookup the torrent again.
func (t *torrent) Metadata() Metadata {
	return Metadata{
		DisplayName: t.displayName,
		InfoHash:    t.infoHash,
		ChunkSize:   int(t.chunkSize),
		InfoBytes:   t.metadataBytes,
		// Trackers: TODO
	}
}

// Tune
func (t *torrent) Tune(tuning ...Tuner) error {
	for _, opt := range tuning {
		opt(t)
	}

	return nil
}

func (t *torrent) locker() sync.Locker {
	return tlocker{torrent: t}
}

func (t *torrent) _lock(depth int) {
	// updated := atomic.AddUint64(&t.lcount, 1)
	// l2.Output(depth, fmt.Sprintf("t(%p) lock initiated - %d", t, updated))
	t._mu.Lock()
	// l2.Output(depth, fmt.Sprintf("t(%p) lock completed - %d", t, updated))
}

func (t *torrent) _unlock(depth int) {
	// updated := atomic.AddUint64(&t.ucount, 1)
	// l2.Output(depth, fmt.Sprintf("t(%p) unlock initiated - %d", t, updated))
	t._mu.Unlock()
	// l2.Output(depth, fmt.Sprintf("t(%p) unlock completed - %d", t, updated))
}

func (t *torrent) _rlock(depth int) {
	// updated := atomic.AddUint64(&t.lcount, 1)
	// l2.Output(depth, fmt.Sprintf("t(%p) rlock initiated - %d", t, updated))
	t._mu.RLock()
	// l2.Output(depth, fmt.Sprintf("t(%p) rlock completed - %d", t, updated))
}

func (t *torrent) _runlock(depth int) {
	// updated := atomic.AddUint64(&t.ucount, 1)
	// l2.Output(depth, fmt.Sprintf("t(%p) unlock initiated - %d", t, updated))
	t._mu.RUnlock()
	// l2.Output(depth, fmt.Sprintf("t(%p) unlock completed - %d", t, updated))
}

func (t *torrent) lock() {
	t._lock(3)
}

func (t *torrent) unlock() {
	t._unlock(3)
}

func (t *torrent) rLock() {
	t._rlock(3)
}

func (t *torrent) rUnlock() {
	t._runlock(3)
}

func (t *torrent) tickleReaders() {
	t.event.Broadcast()
}

func (t *torrent) chunkIndexSpec(chunkIndex pp.Integer, piece pieceIndex) chunkSpec {
	return chunkIndexSpec(chunkIndex, t.pieceLength(piece), t.chunkSize)
}

// Returns a channel that is closed when the Torrent is closed.
func (t *torrent) Closed() <-chan struct{} {
	return t.closed.LockedChan(t.locker())
}

// KnownSwarm returns the known subset of the peers in the Torrent's swarm, including active,
// pending, and half-open peers.
func (t *torrent) KnownSwarm() (ks []Peer) {
	// Add pending peers to the list
	t.peers.Each(func(peer Peer) {
		ks = append(ks, peer)
	})

	// Add half-open peers to the list
	for _, peer := range t.halfOpen {
		ks = append(ks, peer)
	}

	// Add active peers to the list
	for conn := range t.conns {
		ks = append(ks, Peer{
			Id:     conn.PeerID,
			IP:     conn.remoteAddr.IP,
			Port:   int(conn.remoteAddr.Port),
			Source: conn.Discovery,
			// > If the connection is encrypted, that's certainly enough to set SupportsEncryption.
			// > But if we're not connected to them with an encrypted connection, I couldn't say
			// > what's appropriate. We can carry forward the SupportsEncryption value as we
			// > received it from trackers/DHT/PEX, or just use the encryption state for the
			// > connection. It's probably easiest to do the latter for now.
			// https://github.com/anacrolix/torrent/pull/188
			SupportsEncryption: conn.headerEncrypted,
		})
	}

	return ks
}

func (t *torrent) setChunkSize(size pp.Integer) {
	t.chunkSize = size
	*t.piecesM = *newChunks(int(size), func(i *metainfo.Info) *metainfo.Info {
		if i == nil {
			return &metainfo.Info{}
		}
		return i
	}(t.info))
	t.chunkPool = &sync.Pool{
		New: func() interface{} {
			b := make([]byte, size)
			return &b
		},
	}
}

func (t *torrent) pieceComplete(piece pieceIndex) bool {
	defer func() {
		if r := recover(); r != nil {
			l2.Println("RECOVERING piece complete", t.piecesM)
		}
	}()

	if t.piecesM == nil {
		return false
	}

	if t.piecesM.completed.IsEmpty() {
		return false
	}

	return t.piecesM.completed.Contains(piece)
}

func (t *torrent) pieceCompleteUncached(piece pieceIndex) storage.Completion {
	return t.pieces[piece].Storage().Completion()
}

// There's a connection to that address already.
func (t *torrent) addrActive(addr string) bool {
	if _, ok := t.halfOpen[addr]; ok {
		return true
	}
	for c := range t.conns {
		ra := c.remoteAddr
		if ra.String() == addr {
			return true
		}
	}
	return false
}

func (t *torrent) unclosedConnsAsSlice() (ret []*connection) {
	ret = make([]*connection, 0, len(t.conns))
	for c := range t.conns {
		if !c.closed.IsSet() {
			ret = append(ret, c)
		}
	}
	return
}

func (t *torrent) addPeer(p Peer) {
	peersAddedBySource.Add(string(p.Source), 1)

	if t.closed.IsSet() {
		return
	}

	if t.cln.badPeerIPPort(p.IP, p.Port) {
		metrics.Add("peers not added because of bad addr", 1)
		return
	}

	if t.peers.Add(p) {
		metrics.Add("peers replaced", 1)
	}

	t.openNewConns()
	for t.peers.Len() > t.config.TorrentPeersHighWater {
		_, ok := t.peers.DeleteMin()
		if ok {
			metrics.Add("excess reserve peers discarded", 1)
		}
	}
}

func (t *torrent) invalidateMetadata() {
	for i := range t.metadataCompletedChunks {
		t.metadataCompletedChunks[i] = false
	}
	t.nameMu.Lock()
	t.info = nil
	t.nameMu.Unlock()
}

func (t *torrent) saveMetadataPiece(index int, data []byte) {
	if t.haveInfo() {
		return
	}

	if index >= len(t.metadataCompletedChunks) {
		t.logger.Printf("%s: ignoring metadata piece %d", t, index)
		return
	}

	copy(t.metadataBytes[(1<<14)*index:], data)
	t.metadataCompletedChunks[index] = true
}

func (t *torrent) metadataPieceCount() int {
	return (len(t.metadataBytes) + (1 << 14) - 1) / (1 << 14)
}

func (t *torrent) haveMetadataPiece(piece int) bool {
	if t.haveInfo() {
		return (1<<14)*piece < len(t.metadataBytes)
	}

	return piece < len(t.metadataCompletedChunks) && t.metadataCompletedChunks[piece]
}

func (t *torrent) metadataSize() int {
	return len(t.metadataBytes)
}

func infoPieceHashes(info *metainfo.Info) (ret [][]byte) {
	for i := 0; i < len(info.Pieces); i += sha1.Size {
		ret = append(ret, info.Pieces[i:i+sha1.Size])
	}

	return ret
}

func (t *torrent) makePieces() {
	t.piecesM = newChunks(int(t.chunkSize), t.info)
	t.piecesM.gracePeriod = t.duplicateRequestTimeout

	hashes := infoPieceHashes(t.info)
	t.pieces = make([]Piece, len(hashes), len(hashes))
	for i, hash := range hashes {
		piece := &t.pieces[i]
		piece.t = t
		piece.index = pieceIndex(i)
		piece.noPendingWrites.L = &piece.pendingWritesMutex
		piece.hash = (*metainfo.Hash)(unsafe.Pointer(&hash[0]))
		files := *t.files
		beginFile := pieceFirstFileIndex(piece.torrentBeginOffset(), files)
		endFile := pieceEndFileIndex(piece.torrentEndOffset(), files)
		piece.files = files[beginFile:endFile]
	}
}

// Returns the index of the first file containing the piece. files must be
// ordered by offset.
func pieceFirstFileIndex(pieceOffset int64, files []*File) int {
	for i, f := range files {
		if f.offset+f.length > pieceOffset {
			return i
		}
	}

	return 0
}

// Returns the index after the last file containing the piece. files must be
// ordered by offset.
func pieceEndFileIndex(pieceEndOffset int64, files []*File) int {
	for i, f := range files {
		if f.offset+f.length >= pieceEndOffset {
			return i + 1
		}
	}
	return 0
}

func (t *torrent) setInfo(info *metainfo.Info) error {
	if err := validateInfo(info); err != nil {
		return fmt.Errorf("bad info: %s", err)
	}

	if t.storageOpener != nil {
		var err error
		t.storage, err = t.storageOpener.OpenTorrent(info, t.infoHash)
		if err != nil {
			return fmt.Errorf("error opening torrent storage: %s", err)
		}
	}

	t.nameMu.Lock()
	t.info = info
	t.nameMu.Unlock()

	t.initFiles()
	t.makePieces()

	return nil
}

func (t *torrent) onSetInfo() {
	for conn := range t.conns {
		if err := conn.setNumPieces(t.numPieces()); err != nil {
			t.logger.Printf("closing connection: %s", err)
			conn.Close()
		}
	}

	for i := range t.pieces {
		t.updatePieceCompletion(pieceIndex(i))
		p := &t.pieces[i]
		if !p.storageCompletionOk {
			t.digests.Enqueue(i)
		}
	}

	t.event.Broadcast()
	t.gotMetainfo.Set()
	t.updateWantPeersEvent()
}

// Called when metadata for a torrent becomes available.
func (t *torrent) setInfoBytes(b []byte) error {
	var info metainfo.Info

	if metainfo.HashBytes(b) != t.infoHash {
		return errors.New("info bytes have wrong hash")
	}

	if err := bencode.Unmarshal(b, &info); err != nil {
		return fmt.Errorf("error unmarshalling info bytes: %s", err)
	}

	if err := t.setInfo(&info); err != nil {
		return err
	}

	t.metadataBytes = b
	t.metadataCompletedChunks = nil

	t.onSetInfo()

	return nil
}

func (t *torrent) haveAllMetadataPieces() bool {
	if t.haveInfo() {
		return true
	}

	if t.metadataCompletedChunks == nil {
		return false
	}

	for _, have := range t.metadataCompletedChunks {
		if !have {
			return false
		}
	}

	return true
}

// TODO: Propagate errors to disconnect peer.
func (t *torrent) setMetadataSize(bytes int) (err error) {
	if t.haveInfo() {
		// We already know the correct metadata size.
		return err
	}

	if bytes <= 0 || bytes > 10000000 { // 10MB, pulled from my ass.
		return errors.New("bad size")
	}

	if t.metadataBytes != nil && len(t.metadataBytes) == int(bytes) {
		return err
	}

	t.metadataBytes = make([]byte, bytes)
	t.metadataCompletedChunks = make([]bool, (bytes+(1<<14)-1)/(1<<14))
	t.metadataChanged.Broadcast()

	for c := range t.conns {
		c.requestPendingMetadata()
	}

	return err
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *torrent) name() string {
	t.nameMu.RLock()
	defer t.nameMu.RUnlock()
	if t.haveInfo() {
		return t.info.Name
	}
	return t.displayName
}

func (t *torrent) pieceState(index pieceIndex) (ret PieceState) {
	p := &t.pieces[index]
	ret.Priority = t.piecePriority(index)
	ret.Completion = p.completion()

	if p.queuedForHash() || p.hashing {
		ret.Checking = true
	}

	if !ret.Complete && t.piecePartiallyDownloaded(index) {
		ret.Partial = true
	}

	return ret
}

func (t *torrent) metadataPieceSize(piece int) int {
	return metadataPieceSize(len(t.metadataBytes), piece)
}

func (t *torrent) newMetadataExtensionMessage(c *connection, msgType int, piece int, data []byte) pp.Message {
	d := map[string]int{
		"msg_type": msgType,
		"piece":    piece,
	}

	if data != nil {
		d["total_size"] = len(t.metadataBytes)
	}

	p := bencode.MustMarshal(d)
	return pp.Message{
		Type:            pp.Extended,
		ExtendedID:      c.PeerExtensionIDs[pp.ExtensionNameMetadata],
		ExtendedPayload: append(p, data...),
	}
}

func (t *torrent) pieceStateRuns() (ret []PieceStateRun) {
	rle := missinggo.NewRunLengthEncoder(func(el interface{}, count uint64) {
		ret = append(ret, PieceStateRun{
			PieceState: el.(PieceState),
			Length:     int(count),
		})
	})

	for index := range t.pieces {
		rle.Append(t.pieceState(pieceIndex(index)), 1)
	}

	rle.Flush()
	return
}

// Produces a small string representing a PieceStateRun.
func pieceStateRunStatusChars(psr PieceStateRun) (ret string) {
	ret = fmt.Sprintf("%d", psr.Length)
	ret += func() string {
		switch psr.Priority {
		case PiecePriorityNext:
			return "N"
		case PiecePriorityNormal:
			return "."
		case PiecePriorityReadahead:
			return "R"
		case PiecePriorityNow:
			return "!"
		case PiecePriorityHigh:
			return "H"
		default:
			return ""
		}
	}()
	if psr.Checking {
		ret += "H"
	}
	if psr.Partial {
		ret += "P"
	}
	if psr.Complete {
		ret += "C"
	}
	if !psr.Ok {
		ret += "?"
	}
	return
}

func (t *torrent) writeStatus(w io.Writer) {
	fmt.Fprintf(w, "Infohash: %s\n", t.infoHash.HexString())
	fmt.Fprintf(w, "Metadata length: %d\n", t.metadataSize())
	if !t.haveInfo() {
		fmt.Fprintf(w, "Metadata have: ")
		for _, h := range t.metadataCompletedChunks {
			fmt.Fprintf(w, "%c", func() rune {
				if h {
					return 'H'
				}
				return '.'
			}())
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Piece length: %s\n", func() string {
		if t.haveInfo() {
			return fmt.Sprint(t.usualPieceSize())
		}
		return "?"
	}())
	if t.info != nil {
		fmt.Fprintf(w, "Num Pieces: %d (%d completed)\n", t.numPieces(), t.numPiecesCompleted())
		fmt.Fprint(w, "Piece States:")
		for _, psr := range t.pieceStateRuns() {
			w.Write([]byte(" "))
			w.Write([]byte(pieceStateRunStatusChars(psr)))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Reader Pieces:")
	t.forReaderOffsetPieces(func(begin, end pieceIndex) (again bool) {
		fmt.Fprintf(w, " %d:%d", begin, end)
		return true
	})
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Enabled trackers:\n")
	func() {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "    URL\tNext announce\tLast announce\n")
		for _, ta := range slices.Sort(slices.FromMapElems(t.trackerAnnouncers), func(l, r *trackerScraper) bool {
			lu := l.u
			ru := r.u
			var luns, runs url.URL = lu, ru
			luns.Scheme = ""
			runs.Scheme = ""
			var ml missinggo.MultiLess
			ml.StrictNext(luns.String() == runs.String(), luns.String() < runs.String())
			ml.StrictNext(lu.String() == ru.String(), lu.String() < ru.String())
			return ml.Less()
		}).([]*trackerScraper) {
			fmt.Fprintf(tw, "    %s\n", ta.statusLine())
		}
		tw.Flush()
	}()

	fmt.Fprintf(w, "DHT Announces: %d\n", t.numDHTAnnounces)

	spew.NewDefaultConfig()
	spew.Fdump(w, t.statsLocked())

	conns := t.connsAsSlice()
	slices.Sort(conns, worseConn)
	for i, c := range conns {
		fmt.Fprintf(w, "%2d. ", i+1)
		c.WriteStatus(w, t)
	}
}

func (t *torrent) haveInfo() bool {
	return t.info != nil
}

// Returns a run-time generated MetaInfo that includes the info bytes and
// announce-list as currently known to the client.
func (t *torrent) newMetaInfo() metainfo.MetaInfo {
	return metainfo.MetaInfo{
		CreationDate: time.Now().Unix(),
		Comment:      "dynamic metainfo from client",
		CreatedBy:    "go.torrent",
		AnnounceList: t.metainfo.UpvertedAnnounceList(),
		InfoBytes: func() []byte {
			if t.haveInfo() {
				return t.metadataBytes
			}

			return nil
		}(),
	}
}

func (t *torrent) BytesMissing() int64 {
	t.rLock()
	defer t.rUnlock()
	return t.bytesMissingLocked()
}

func (t *torrent) bytesMissingLocked() int64 {
	return t.bytesLeft()
}

func (t *torrent) bytesLeft() (left int64) {
	bitmap.Flip(t.piecesM.completed, 0, bitmap.BitIndex(t.numPieces())).IterTyped(func(piece int) bool {
		p := &t.pieces[piece]
		left += int64(p.length() - p.numDirtyBytes())
		return true
	})
	return
}

// Bytes left to give in tracker announces.
func (t *torrent) bytesLeftAnnounce() int64 {
	if t.haveInfo() {
		return t.bytesLeft()
	}

	return -1
}

func (t *torrent) piecePartiallyDownloaded(piece pieceIndex) bool {
	if t.pieceComplete(piece) {
		return false
	}
	if t.pieceAllDirty(piece) {
		return false
	}
	return t.pieces[piece].hasDirtyChunks()
}

func (t *torrent) usualPieceSize() int {
	return int(t.info.PieceLength)
}

func (t *torrent) numPieces() pieceIndex {
	return pieceIndex(t.info.NumPieces())
}

func (t *torrent) numPiecesCompleted() (num int) {
	return t.piecesM.completed.Len()
}

func (t *torrent) close() (err error) {
	t.lock()
	defer t.unlock()

	t.closed.Set()
	t.tickleReaders()

	if t.storage != nil {
		t.storageLock.Lock()
		t.storage.Close()
		t.storageLock.Unlock()
	}

	for conn := range t.conns {
		conn.Close()
	}

	t.event.Broadcast()
	// t.pieceStateChanges.Close()

	t.updateWantPeersEvent()
	return err
}

func (t *torrent) requestOffset(r request) int64 {
	return torrentRequestOffset(t.info.Length, int64(t.usualPieceSize()), r)
}

// Return the request that would include the given offset into the torrent
// data. Returns !ok if there is no such request.
func (t *torrent) offsetRequest(off int64) (req request, ok bool) {
	return torrentOffsetRequest(t.info.Length, t.info.PieceLength, int64(t.chunkSize), off)
}

func (t *torrent) writeChunk(piece int, begin int64, data []byte) (err error) {
	defer perf.ScopeTimerErr(&err)()
	n, err := t.pieces[piece].Storage().WriteAt(data, begin)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}

	return err
}

func (t *torrent) pieceNumChunks(piece pieceIndex) pp.Integer {
	return (t.pieceLength(piece) + t.chunkSize - 1) / t.chunkSize
}

func (t *torrent) pendAllChunkSpecs(pieceIndex pieceIndex) {
	t.pieces[pieceIndex].dirtyChunks.Clear()
}

func (t *torrent) pieceLength(piece pieceIndex) pp.Integer {
	if t.info.PieceLength == 0 {
		// There will be no variance amongst pieces. Only pain.
		return 0
	}

	if piece == t.numPieces()-1 {
		ret := pp.Integer(t.info.Length % t.info.PieceLength)
		if ret != 0 {
			return ret
		}
	}
	return pp.Integer(t.info.PieceLength)
}

func (t *torrent) haveAnyPieces() bool {
	return t.piecesM.completed.Len() != 0
}

func (t *torrent) haveAllPieces() bool {
	if !t.haveInfo() {
		return false
	}
	return t.piecesM.completed.Len() == bitmap.BitIndex(t.numPieces())
}

func (t *torrent) havePiece(index pieceIndex) bool {
	return t.haveInfo() && t.pieceComplete(index)
}

func (t *torrent) haveChunk(r request) (ret bool) {
	if !t.haveInfo() {
		return false
	}
	if t.pieceComplete(pieceIndex(r.Index)) {
		return true
	}
	p := &t.pieces[r.Index]
	return !p.pendingChunk(r.chunkSpec, t.chunkSize)
}

func chunkIndex(cs chunkSpec, chunkSize pp.Integer) int {
	return int(cs.Begin / chunkSize)
}

func (t *torrent) wantPieceIndex(index pieceIndex) bool {
	if !t.haveInfo() {
		return false
	}
	if index < 0 || index >= t.numPieces() {
		return false
	}
	p := &t.pieces[index]
	if p.queuedForHash() {
		return false
	}
	if p.hashing {
		return false
	}
	if t.pieceComplete(index) {
		return false
	}

	if t.piecesM.ChunksMissing(index) {
		return true
	}

	return !t.forReaderOffsetPieces(func(begin, end pieceIndex) bool {
		return index < begin || index >= end
	})
}

// The worst connection is one that hasn't been sent, or sent anything useful
// for the longest. A bad connection is one that usually sends us unwanted
// pieces, or has been in worser half of the established connections for more
// than a minute.
func (t *torrent) worstBadConn() *connection {
	wcs := worseConnSlice{t.unclosedConnsAsSlice()}
	heap.Init(&wcs)
	for wcs.Len() != 0 {
		c := heap.Pop(&wcs).(*connection)
		if c.stats.ChunksReadWasted.Int64() >= 6 && c.stats.ChunksReadWasted.Int64() > c.stats.ChunksReadUseful.Int64() {
			return c
		}
		// If the connection is in the worst half of the established
		// connection quota and is older than a minute.
		if wcs.Len() >= (t.maxEstablishedConns+1)/2 {
			// Give connections 1 minute to prove themselves.
			if time.Since(c.completedHandshake) > time.Minute {
				return c
			}
		}
	}
	return nil
}

// PieceStateChange ...
type PieceStateChange struct {
	Index int
	PieceState
}

func (t *torrent) publishPieceChange(piece pieceIndex) {
	cur := t.pieceState(piece)
	p := &t.pieces[piece]

	p.publicPieceState = cur
	t.pieceStateChanges.Publish(PieceStateChange{
		int(piece),
		cur,
	})

	t.event.Broadcast()
	t.cln.event.Broadcast() // cause the client to detect completed torrents.
}

func (t *torrent) pieceNumPendingChunks(piece pieceIndex) pp.Integer {
	if t.pieceComplete(piece) {
		return 0
	}
	return t.pieceNumChunks(piece) - t.pieces[piece].numDirtyChunks()
}

func (t *torrent) pieceAllDirty(piece pieceIndex) bool {
	return t.pieces[piece].dirtyChunks.Len() == int(t.pieceNumChunks(piece))
}

func (t *torrent) readersChanged() {
	t.updateReaderPieces()
	t.updateAllPiecePriorities()
}

func (t *torrent) updateReaderPieces() {
	t.readerNowPieces, t.readerReadaheadPieces = t.readerPiecePriorities()
}

func (t *torrent) readerPosChanged(from, to pieceRange) {
	if from == to {
		return
	}

	t.updateReaderPieces()

	// Order the ranges, high and low.
	l, h := from, to
	if l.begin > h.begin {
		l, h = h, l
	}

	if l.end < h.begin {
		// Two distinct ranges.
		t.updatePiecePriorities(l.begin, l.end)
		t.updatePiecePriorities(h.begin, h.end)
	} else {
		// Ranges overlap.
		end := l.end
		if h.end > end {
			end = h.end
		}
		t.updatePiecePriorities(l.begin, end)
	}
}

func (t *torrent) maybeNewConns() {
	// Tickle the accept routine.
	t.cln.event.Broadcast()
	t.openNewConns()
}

func (t *torrent) piecePriorityChanged(piece pieceIndex) {
	for c := range t.conns {
		if c.updatePiecePriority(piece) {
			c.updateRequests()
		}
	}
	t.maybeNewConns()
	t.publishPieceChange(piece)
}

func (t *torrent) updatePiecePriority(piece pieceIndex) {
	p := &t.pieces[piece]
	newPrio := p.uncachedPriority()

	if newPrio == PiecePriorityNone {
		if t.piecesM.ChunksComplete(piece) {
			return
		}
		// l2.Output(2,
		// 	fmt.Sprintf(
		// 		"piece %d not complete, pending chunks: completed(%t) - queued hashing(%t) - hashing (%t)\n",
		// 		p.index,
		// 		p.t.pieceComplete(p.index),
		// 		p.t.pieceQueuedForHash(p.index),
		// 		p.t.hashingPiece(p.index),
		// 	),
		// )
	}

	if !t.piecesM.ChunksAdjust(piece, newPrio.BitmapPriority()) {
		// l2.Output(2, fmt.Sprintf("chunks not adjusted: %d - %d\n", piece, piece))
		return
	}

	t.piecePriorityChanged(piece)
}

func (t *torrent) updateAllPiecePriorities() {
	t.updatePiecePriorities(0, t.numPieces())
}

// Update all piece priorities in one hit. This function should have the same
// output as updatePiecePriority, but across all pieces.
func (t *torrent) updatePiecePriorities(begin, end pieceIndex) {
	for i := begin; i < end; i++ {
		t.updatePiecePriority(i)
	}
}

// Returns the range of pieces [begin, end) that contains the extent of bytes.
func (t *torrent) byteRegionPieces(off, size int64) (begin, end pieceIndex) {
	if off >= t.info.Length {
		return
	}
	if off < 0 {
		size += off
		off = 0
	}
	if size <= 0 {
		return
	}
	begin = pieceIndex(off / t.info.PieceLength)
	end = pieceIndex((off + size + t.info.PieceLength - 1) / t.info.PieceLength)
	if end > pieceIndex(t.info.NumPieces()) {
		end = pieceIndex(t.info.NumPieces())
	}
	return
}

// Returns true if all iterations complete without breaking. Returns the read
// regions for all readers. The reader regions should not be merged as some
// callers depend on this method to enumerate readers.
func (t *torrent) forReaderOffsetPieces(f func(begin, end pieceIndex) (more bool)) (all bool) {
	for r := range t.readers {
		p := r.pieces
		if p.begin >= p.end {
			continue
		}
		if !f(p.begin, p.end) {
			return false
		}
	}
	return true
}

func (t *torrent) piecePriority(piece pieceIndex) piecePriority {
	return piecePriority(t.piecesM.Priority(piece))
}

func (t *torrent) pendRequest(req request) {
	ci := chunkIndex(req.chunkSpec, t.chunkSize)
	t.pieces[req.Index].pendChunkIndex(ci)
}

func (t *torrent) pieceCompletionChanged(piece pieceIndex) {
	t.tickleReaders()
	t.event.Broadcast()
	if t.pieceComplete(piece) {
		t.onPieceCompleted(piece)
	} else {
		t.onIncompletePiece(piece)
	}
	t.updatePiecePriority(piece)
}

func (t *torrent) numReceivedConns() (ret int) {
	for c := range t.conns {
		if c.Discovery == peerSourceIncoming {
			ret++
		}
	}
	return
}

func (t *torrent) maxHalfOpen() int {
	// Note that if we somehow exceed the maximum established conns, we want
	// the negative value to have an effect.
	establishedHeadroom := int64(t.maxEstablishedConns - len(t.conns))
	extraIncoming := int64(t.numReceivedConns() - t.maxEstablishedConns/2)
	// We want to allow some experimentation with new peers, and to try to
	// upset an oversupply of received connections.
	return int(min(max(5, extraIncoming)+establishedHeadroom, int64(t.config.HalfOpenConnsPerTorrent)))
}

func (t *torrent) openNewConns() {
	defer t.updateWantPeersEvent()
	for t.peers.Len() != 0 {
		if !t.wantConns() {
			return
		}

		if len(t.halfOpen) >= t.maxHalfOpen() {
			return
		}

		t.initiateConn(t.peers.PopMax())
	}
}

func (t *torrent) getConnPieceInclination() []int {
	_ret := t.connPieceInclinationPool.Get()
	if _ret == nil {
		pieceInclinationsNew.Add(1)
		return rand.Perm(int(t.numPieces()))
	}
	pieceInclinationsReused.Add(1)
	return *_ret.(*[]int)
}

func (t *torrent) putPieceInclination(pi []int) {
	t.connPieceInclinationPool.Put(&pi)
	pieceInclinationsPut.Add(1)
}

func (t *torrent) updatePieceCompletion(piece pieceIndex) bool {
	p := t.piece(piece)
	uncached := t.pieceCompleteUncached(piece)
	cached := p.completion()
	changed := cached != uncached
	p.storageCompletionOk = uncached.Ok

	if uncached.Complete {
		t.piecesM.Complete(piece)
	} else {
		t.piecesM.ChunksPend(piece)
		t.piecesM.ChunksRelease(piece)
	}

	if changed {
		log.Fstr("piece %d completion changed: %+v -> %+v", piece, cached, uncached).WithValues(debugLogValue).Log(t.logger)
		t.pieceCompletionChanged(piece)
	}
	return changed
}

// Non-blocking read. Client lock is not required.
func (t *torrent) readAt(b []byte, off int64) (n int, err error) {
	p := &t.pieces[off/t.info.PieceLength]
	p.waitNoPendingWrites()
	return p.Storage().ReadAt(b, off-p.Info().Offset())
}

func (t *torrent) updateAllPieceCompletions() {
	for i := pieceIndex(0); i < t.numPieces(); i++ {
		t.updatePieceCompletion(i)
	}
}

// Returns an error if the metadata was completed, but couldn't be set for
// some reason. Blame it on the last peer to contribute.
func (t *torrent) maybeCompleteMetadata() error {
	if t.haveInfo() {
		// Nothing to do.
		return nil
	}

	if !t.haveAllMetadataPieces() {
		// Don't have enough metadata pieces.
		return nil
	}

	if err := t.setInfoBytes(t.metadataBytes); err != nil {
		t.invalidateMetadata()
		return fmt.Errorf("error setting info bytes: %s", err)
	}

	if t.config.Debug {
		t.logger.Printf("%s: got metadata from peers", t)
	}

	return nil
}

func (t *torrent) readerPiecePriorities() (now, readahead bitmap.Bitmap) {
	t.forReaderOffsetPieces(func(begin, end pieceIndex) bool {
		if end > begin {
			now.Add(bitmap.BitIndex(begin))
			readahead.AddRange(bitmap.BitIndex(begin)+1, bitmap.BitIndex(end))
		}
		return true
	})
	return
}

func (t *torrent) needData() bool {
	if t.closed.IsSet() {
		return false
	}

	if !t.haveInfo() {
		return true
	}

	return t.piecesM.Missing() != 0
}

func appendMissingStrings(old, new []string) (ret []string) {
	ret = old
new:
	for _, n := range new {
		for _, o := range old {
			if o == n {
				continue new
			}
		}
		ret = append(ret, n)
	}
	return
}

func appendMissingTrackerTiers(existing [][]string, minNumTiers int) (ret [][]string) {
	ret = existing
	for minNumTiers > len(ret) {
		ret = append(ret, nil)
	}
	return
}

func (t *torrent) addTrackers(announceList [][]string) {
	fullAnnounceList := &t.metainfo.AnnounceList
	t.metainfo.AnnounceList = appendMissingTrackerTiers(*fullAnnounceList, len(announceList))
	for tierIndex, trackerURLs := range announceList {
		(*fullAnnounceList)[tierIndex] = appendMissingStrings((*fullAnnounceList)[tierIndex], trackerURLs)
	}
	t.startMissingTrackerScrapers()
	t.updateWantPeersEvent()
}

// Don't call this before the info is available.
func (t *torrent) bytesCompleted() int64 {
	if !t.haveInfo() {
		return 0
	}
	return t.info.TotalLength() - t.bytesLeft()
}

func (t *torrent) SetInfoBytes(b []byte) (err error) {
	t.lock()
	defer t.unlock()
	return t.setInfoBytes(b)
}

// Returns true if connection is removed from torrent.Conns.
func (t *torrent) deleteConnection(c *connection) (ret bool) {
	c.Close()
	t.lock()
	_, ret = t.conns[c]
	delete(t.conns, c)
	t.unlock()
	metrics.Add("deleted connections", 1)
	c.deleteAllRequests()
	if len(t.conns) == 0 {
		t.assertNoPendingRequests()
	}

	return ret
}

func (t *torrent) assertNoPendingRequests() {
	if outstanding := t.piecesM.Outstanding(); len(outstanding) != 0 {
		for i, r := range outstanding {
			l2.Println("still expecting", i, r.Index, r.Begin, r.Length)
		}
		panic(outstanding)
	}
}

func (t *torrent) dropConnection(c *connection) {
	c.Close()
	if t.deleteConnection(c) {
		t.openNewConns()
	}

	t.event.Broadcast()
}

func (t *torrent) wantPeers() bool {
	if t.closed.IsSet() || t.wantPeersEvent.IsSet() {
		return false
	}

	if t.peers.Len() > t.config.TorrentPeersLowWater {
		return false
	}

	return t.needData() || t.seeding()
}

func (t *torrent) updateWantPeersEvent() {
	if t.wantPeers() {
		t.wantPeersEvent.Set()
	} else {
		t.wantPeersEvent.Clear()
	}
}

// Returns whether the client should make effort to seed the torrent.
func (t *torrent) seeding() bool {
	if t.closed.IsSet() {
		return false
	}
	if t.config.NoUpload {
		return false
	}
	if !t.config.Seed {
		return false
	}
	if t.config.DisableAggressiveUpload && t.needData() {
		return false
	}
	return true
}

func (t *torrent) startScrapingTracker(_url string) {
	if _url == "" {
		return
	}

	u, err := url.Parse(_url)
	if err != nil {
		// URLs with a leading '*' appear to be a uTorrent convention to
		// disable trackers.
		if _url[0] != '*' {
			log.Str("error parsing tracker url").AddValues("url", _url).Log(t.logger)
		}
		return
	}

	if u.Scheme == "udp" {
		u.Scheme = "udp4"
		t.startScrapingTracker(u.String())
		u.Scheme = "udp6"
		t.startScrapingTracker(u.String())
		return
	}

	if u.Scheme == "udp4" && (t.config.DisableIPv4Peers || t.config.DisableIPv4) {
		return
	}

	if u.Scheme == "udp6" && t.config.DisableIPv6 {
		return
	}

	if _, ok := t.trackerAnnouncers[_url]; ok {
		return
	}

	newAnnouncer := &trackerScraper{
		u: *u,
		t: t,
	}

	if t.trackerAnnouncers == nil {
		t.trackerAnnouncers = make(map[string]*trackerScraper)
	}
	t.trackerAnnouncers[_url] = newAnnouncer
	go newAnnouncer.Run()
}

// Adds and starts tracker scrapers for tracker URLs that aren't already
// running.
func (t *torrent) startMissingTrackerScrapers() {
	if t.config.DisableTrackers {
		return
	}

	t.startScrapingTracker(t.metainfo.Announce)

	for _, tier := range t.metainfo.AnnounceList {
		for _, url := range tier {
			t.startScrapingTracker(url)
		}
	}
}

// Returns an AnnounceRequest with fields filled out to defaults and current
// values.
func (t *torrent) announceRequest(event tracker.AnnounceEvent) tracker.AnnounceRequest {
	// Note that IPAddress is not set. It's set for UDP inside the tracker
	// code, since it's dependent on the network in use.
	return tracker.AnnounceRequest{
		Event:    event,
		NumWant:  -1,
		Port:     uint16(t.cln.incomingPeerPort()),
		PeerId:   t.cln.peerID,
		InfoHash: t.infoHash,
		Key:      t.cln.announceKey(),

		// The following are vaguely described in BEP 3.

		Left:     t.bytesLeftAnnounce(),
		Uploaded: t.stats.BytesWrittenData.Int64(),
		// There's no mention of wasted or unwanted download in the BEP.
		Downloaded: t.stats.BytesReadUsefulData.Int64(),
	}
}

// Adds peers revealed in an announce until the announce ends, or we have
// enough peers.
func (t *torrent) consumeDhtAnnouncePeers(pvs <-chan dht.PeersValues) {
	for v := range pvs {
		for _, cp := range v.Peers {
			if cp.Port == 0 {
				// Can't do anything with this.
				continue
			}
			t.lock()
			t.addPeer(Peer{
				IP:     cp.IP[:],
				Port:   cp.Port,
				Source: peerSourceDhtGetPeers,
			})
			t.unlock()
		}
	}
}

func (t *torrent) announceToDht(impliedPort bool, s *dht.Server) error {
	ps, err := s.Announce(t.infoHash, t.cln.incomingPeerPort(), impliedPort)
	if err != nil {
		return err
	}
	go t.consumeDhtAnnouncePeers(ps.Peers)
	select {
	case <-t.closed.LockedChan(t.locker()):
	case <-time.After(5 * time.Minute):
	}
	ps.Close()
	return nil
}

func (t *torrent) dhtAnnouncer(s *dht.Server) {
	for {
		select {
		case <-t.closed.LockedChan(t.locker()):
			return
		case <-t.wantPeersEvent.LockedChan(t.locker()):
		}
		t.lock()
		t.numDHTAnnounces++
		t.unlock()
		err := t.announceToDht(true, s)
		if err != nil {
			t.logger.Printf("error announcing %q to DHT: %s", t, err)
			time.Sleep(time.Second)
		}
	}
}

func (t *torrent) addPeers(peers []Peer) {
	for _, p := range peers {
		t.addPeer(p)
	}
}

func (t *torrent) Stats() TorrentStats {
	t.rLock()
	defer t.rUnlock()
	return t.statsLocked()
}

func (t *torrent) statsLocked() (ret TorrentStats) {
	ret.ActivePeers = len(t.conns)
	ret.HalfOpenPeers = len(t.halfOpen)
	ret.PendingPeers = t.peers.Len()
	ret.TotalPeers = t.numTotalPeers()
	ret.ConnectedSeeders = 0

	for c := range t.conns {
		if all, ok := c.peerHasAllPieces(); all && ok {
			ret.ConnectedSeeders++
		}
	}

	ret.ConnStats = t.stats.Copy()
	return
}

// The total number of peers in the torrent.
func (t *torrent) numTotalPeers() int {
	peers := make(map[string]struct{})

	for conn := range t.conns {
		if conn == nil {
			continue
		}

		ra := conn.conn.RemoteAddr()
		if ra == nil {
			// It's been closed and doesn't support RemoteAddr.
			continue
		}
		peers[ra.String()] = struct{}{}
	}

	for addr := range t.halfOpen {
		peers[addr] = struct{}{}
	}

	t.peers.Each(func(peer Peer) {
		peers[fmt.Sprintf("%s:%d", peer.IP, peer.Port)] = struct{}{}
	})

	return len(peers)
}

// Reconcile bytes transferred before connection was associated with a
// torrent.
func (t *torrent) reconcileHandshakeStats(c *connection) {
	if c.stats != (ConnStats{
		// Handshakes should only increment these fields:
		BytesWritten: c.stats.BytesWritten,
		BytesRead:    c.stats.BytesRead,
	}) {
		panic("bad stats")
	}
	c.postHandshakeStats(func(cs *ConnStats) {
		cs.BytesRead.Add(c.stats.BytesRead.Int64())
		cs.BytesWritten.Add(c.stats.BytesWritten.Int64())
	})
	c.reconciledHandshakeStats = true
}

// Returns true if the connection is added.
func (t *torrent) addConnection(c *connection) (err error) {
	t.lock()
	defer t.unlock()

	defer func() {
		if err == nil {
			metrics.Add("added connections", 1)
		}
	}()

	if t.closed.IsSet() {
		return errors.New("torrent closed 2")
	}

	for c0 := range t.conns {
		if c.PeerID != c0.PeerID {
			continue
		}

		if !t.config.dropDuplicatePeerIds {
			continue
		}

		if left, ok := c.hasPreferredNetworkOver(c0); ok && left {
			c0.Close()
			t.deleteConnection(c0)
		} else {
			return errors.New("existing connection preferred")
		}
	}

	if len(t.conns) >= t.maxEstablishedConns {
		c := t.worstBadConn()
		if c == nil {
			return errors.New("don't want conns")
		}
		c.Close()
		t.deleteConnection(c)
	}

	if len(t.conns) >= t.maxEstablishedConns {
		panic(len(t.conns))
	}
	t.conns[c] = struct{}{}

	return nil
}

func (t *torrent) wantConns() bool {
	if !t.networkingEnabled {
		return false
	}

	if t.closed.IsSet() {
		return false
	}

	if !t.seeding() && !t.needData() {
		return false
	}

	if len(t.conns) < t.maxEstablishedConns {
		return true
	}

	return t.worstBadConn() != nil
}

func (t *torrent) SetMaxEstablishedConns(max int) (oldMax int) {
	t.lock()
	defer t.unlock()
	oldMax = t.maxEstablishedConns
	t.maxEstablishedConns = max
	wcs := slices.HeapInterface(slices.FromMapKeys(t.conns), worseConn)
	for len(t.conns) > t.maxEstablishedConns && wcs.Len() > 0 {
		t.dropConnection(wcs.Pop().(*connection))
	}
	t.openNewConns()
	return oldMax
}

func (t *torrent) pieceHashed(piece pieceIndex, failure error) {
	correct := failure == nil
	t.logger.Log(log.Fstr("hashed piece %d (passed=%t)", piece, correct).WithValues(debugLogValue))
	p := t.piece(piece)
	p.numVerifies++
	t.event.Broadcast()

	if t.closed.IsSet() {
		return
	}

	// Don't score the first time a piece is hashed, it could be an
	// initial check.
	if p.storageCompletionOk {
		if correct {
			pieceHashedCorrect.Add(1)
		} else {
			log.Fmsg("piece %d failed hash", piece).AddValues(t, p).Log(t.logger)
			pieceHashedNotCorrect.Add(1)
		}
	}

	if correct {
		// Don't increment stats above connection-level for every involved
		// connection.
		t.allStats((*ConnStats).incrementPiecesDirtiedGood)
		// TODO: implement this at the connection level.
		// for _, c := range touchers {
		// 	c.stats.incrementPiecesDirtiedGood()
		// }

		err := p.Storage().MarkComplete()
		if err != nil {
			t.piecesM.ChunksPend(piece)
			t.piecesM.ChunksRelease(piece)
			t.logger.Printf("%T: error marking piece complete %d: %s", t.storage, piece, err)
		}
	} else {
		t.piecesM.ChunksPend(piece)
		t.piecesM.ChunksRelease(piece)
		t.piecesM.ChunksFailed(piece)

		t.allStats((*ConnStats).incrementPiecesDirtiedBad)

		t.onIncompletePiece(piece)
		p.Storage().MarkNotComplete()
	}
}

func (t *torrent) cancelRequestsForPiece(piece pieceIndex) {
	t.rLock()
	defer t.rUnlock()
	// TODO: Make faster
	for cn := range t.conns {
		cn.updateRequests()
	}
}

func (t *torrent) onPieceCompleted(piece pieceIndex) {
	t.pendAllChunkSpecs(piece)
	t.cancelRequestsForPiece(piece)

	for conn := range t.conns {
		conn.Have(piece)
	}
}

// Called when a piece is found to be not complete.
func (t *torrent) onIncompletePiece(piece pieceIndex) {
	if t.pieceAllDirty(piece) {
		t.pendAllChunkSpecs(piece)
	}

	if !t.wantPieceIndex(piece) {
		return
	}

	// We could drop any connections that we told we have a piece that we
	// don't here. But there's a test failure, and it seems clients don't care
	// if you request pieces that you already claim to have. Pruning bad
	// connections might just remove any connections that aren't treating us
	// favourably anyway.

	for conn := range t.conns {
		if conn.PeerHasPiece(piece) {
			conn.updateRequests()
		}
	}
}

func (t *torrent) connsAsSlice() (ret []*connection) {
	for c := range t.conns {
		ret = append(ret, c)
	}
	return
}

// Forces all the pieces to be re-hashed. See also Piece.VerifyData. This should not be called
// before the Info is available.
func (t *torrent) VerifyData() {
	for i := pieceIndex(0); i < t.NumPieces(); i++ {
		t.Piece(i).VerifyData()
	}
}

// Start the process of connecting to the given peer for the given torrent if
// appropriate.
func (t *torrent) initiateConn(peer Peer) {
	if peer.Id == t.cln.peerID {
		return
	}

	if t.cln.badPeerIPPort(peer.IP, peer.Port) && !peer.Trusted {
		return
	}

	addr := IpPort{IP: peer.IP, Port: uint16(peer.Port)}
	if t.addrActive(addr.String()) {
		return
	}

	t.halfOpen[addr.String()] = peer
	go t.cln.outgoingConnection(t, addr, peer.Source, peer.Trusted)
}

// Adds each a trusted, pending peer for each of the Client's addresses.
func (t *torrent) AddClientPeer(cl *Client) {
	t.AddPeers(func() (ps []Peer) {
		for _, la := range cl.ListenAddrs() {
			ps = append(ps, Peer{
				IP:      missinggo.AddrIP(la),
				Port:    missinggo.AddrPort(la),
				Trusted: true,
			})
		}
		return
	}())
}

// All stats that include this Torrent. Useful when we want to increment
// ConnStats but not for every connection.
func (t *torrent) allStats(f func(*ConnStats)) {
	f(&t.stats)
	f(&t.cln.stats)
}

func (t *torrent) dialTimeout() time.Duration {
	return reducedDialTimeout(t.config.MinDialTimeout, t.config.NominalDialTimeout, t.config.HalfOpenConnsPerTorrent, t.peers.Len())
}

func (t *torrent) piece(i int) *Piece {
	return &t.pieces[i]
}

// The torrent's infohash. This is fixed and cannot change. It uniquely
// identifies a torrent.
func (t *torrent) InfoHash() metainfo.Hash {
	return t.infoHash
}

// Returns a channel that is closed when the info (.Info()) for the torrent
// has become available.
func (t *torrent) GotInfo() <-chan struct{} {
	t.rLock()
	defer t.rUnlock()
	return t.gotMetainfo.C()
}

// Returns the metainfo info dictionary, or nil if it's not yet available.
func (t *torrent) Info() *metainfo.Info {
	t.rLock()
	defer t.rUnlock()
	return t.info
}

// Returns a Reader bound to the torrent's data. All read calls block until
// the data requested is actually available.
func (t *torrent) NewReader() Reader {
	r := reader{
		mu:        t.locker(),
		t:         t,
		readahead: 5 * 1024 * 1024,
		length:    t.info.Length,
	}
	t.addReader(&r)
	return &r
}

// Returns the state of pieces of the torrent. They are grouped into runs of
// same state. The sum of the state run lengths is the number of pieces
// in the torrent.
func (t *torrent) PieceStateRuns() []PieceStateRun {
	t.rLock()
	defer t.rUnlock()
	return t.pieceStateRuns()
}

func (t *torrent) PieceState(piece pieceIndex) PieceState {
	t.rLock()
	defer t.rUnlock()
	return t.pieceState(piece)
}

// The number of pieces in the torrent. This requires that the info has been
// obtained first.
func (t *torrent) NumPieces() pieceIndex {
	return t.numPieces()
}

// Get missing bytes count for specific piece.
func (t *torrent) PieceBytesMissing(piece int) int64 {
	t.lock()
	defer t.unlock()

	return int64(t.pieces[piece].bytesLeft())
}

// Number of bytes of the entire torrent we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces. Do not use this
// for download rate, as it can go down when pieces are lost or fail checks.
// Sample Torrent.Stats.DataBytesRead for actual file data download rate.
func (t *torrent) BytesCompleted() int64 {
	t.rLock()
	defer t.rUnlock()
	return t.bytesCompleted()
}

// The subscription emits as (int) the index of pieces as their state changes.
// A state change is when the PieceState for a piece alters in value.
func (t *torrent) SubscribePieceStateChanges() *pubsub.Subscription {
	return t.pieceStateChanges.Subscribe()
}

// Returns true if the torrent is currently being seeded. This occurs when the
// client is willing to upload without wanting anything in return.
func (t *torrent) Seeding() bool {
	t.lock()
	defer t.unlock()
	return t.seeding()
}

// Clobbers the torrent display name. The display name is used as the torrent
// name if the metainfo is not available.
func (t *torrent) SetDisplayName(dn string) {
	t.nameMu.Lock()
	defer t.nameMu.Unlock()
	t.displayName = dn
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *torrent) Name() string {
	return t.name()
}

// The completed length of all the torrent data, in all its files. This is
// derived from the torrent info, when it is available.
func (t *torrent) Length() int64 {
	return t.info.Length
}

// Returns a run-time generated metainfo for the torrent that includes the
// info bytes and announce-list as currently known to the client.
func (t *torrent) Metainfo() metainfo.MetaInfo {
	t.lock()
	defer t.unlock()
	return t.newMetaInfo()
}

func (t *torrent) addReader(r *reader) {
	t.lock()
	defer t.unlock()
	if t.readers == nil {
		t.readers = make(map[*reader]struct{})
	}
	t.readers[r] = struct{}{}
	r.posChanged()
}

func (t *torrent) deleteReader(r *reader) {
	delete(t.readers, r)
	t.readersChanged()
}

// Raise the priorities of pieces in the range [begin, end) to at least Normal
// priority. Piece indexes are not the same as bytes. Requires that the info
// has been obtained, see Torrent.Info and Torrent.GotInfo.
func (t *torrent) DownloadPieces(begin, end pieceIndex) {
	t.lock()
	defer t.unlock()
	t.downloadPiecesLocked(begin, end)
}

func (t *torrent) downloadPiecesLocked(begin, end pieceIndex) {
	for i := begin; i < end; i++ {
		if t.pieces[i].priority.Raise(PiecePriorityNormal) {
			t.updatePiecePriority(i)
		}
	}
}

func (t *torrent) CancelPieces(begin, end pieceIndex) {
	t.lock()
	defer t.unlock()
	t.cancelPiecesLocked(begin, end)
}

func (t *torrent) cancelPiecesLocked(begin, end pieceIndex) {
	for i := begin; i < end; i++ {
		p := &t.pieces[i]
		if p.priority == PiecePriorityNone {
			continue
		}
		p.priority = PiecePriorityNone
		t.updatePiecePriority(i)
	}
}

func (t *torrent) initFiles() {
	var offset int64
	t.files = new([]*File)
	for _, fi := range t.info.UpvertedFiles() {
		var path []string
		if len(fi.PathUTF8) != 0 {
			path = fi.PathUTF8
		} else {
			path = fi.Path
		}
		*t.files = append(*t.files, &File{
			t,
			strings.Join(append([]string{t.info.Name}, path...), "/"),
			offset,
			fi.Length,
			fi,
			PiecePriorityNone,
		})
		offset += fi.Length
	}
}

// Returns handles to the files in the torrent. This requires that the Info is
// available first.
func (t *torrent) Files() []*File {
	return *t.files
}

func (t *torrent) AddPeers(pp []Peer) {
	t.lock()
	defer t.unlock()
	t.addPeers(pp)
}

// Marks the entire torrent for download. Requires the info first, see
// GotInfo. Sets piece priorities for historical reasons.
func (t *torrent) DownloadAll() {
	t.DownloadPieces(0, t.numPieces())
}

func (t *torrent) String() string {
	if s := t.name(); s != "" {
		return strconv.Quote(s)
	}

	return t.infoHash.HexString()
}

func (t *torrent) AddTrackers(announceList [][]string) {
	t.lock()
	defer t.unlock()
	t.addTrackers(announceList)
}

func (t *torrent) Piece(i pieceIndex) *Piece {
	return t.piece(i)
}

func (t *torrent) ping(addr net.UDPAddr) {
	t.cln.eachDhtServer(func(s *dht.Server) {
		go s.Ping(&addr, nil)
	})
}

func (t *torrent) publicAddr(ip net.IP) IpPort {
	return t.cln.publicAddr(ip)
}

// Process incoming ut_metadata message.
func (t *torrent) gotMetadataExtensionMsg(payload []byte, c *connection) error {
	var d map[string]int
	err := bencode.Unmarshal(payload, &d)
	if _, ok := err.(bencode.ErrUnusedTrailingBytes); ok {
	} else if err != nil {
		return fmt.Errorf("error unmarshalling bencode: %s", err)
	}
	msgType, ok := d["msg_type"]
	if !ok {
		return errors.New("missing msg_type field")
	}
	piece := d["piece"]
	switch msgType {
	case pp.DataMetadataExtensionMsgType:
		c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.MetadataChunksRead }))
		if !c.requestedMetadataPiece(piece) {
			return fmt.Errorf("got unexpected piece %d", piece)
		}
		c.metadataRequests[piece] = false
		begin := len(payload) - metadataPieceSize(d["total_size"], piece)
		if begin < 0 || begin >= len(payload) {
			return fmt.Errorf("data has bad offset in payload: %d", begin)
		}
		t.saveMetadataPiece(piece, payload[begin:])
		c.lastUsefulChunkReceived = time.Now()
		return t.maybeCompleteMetadata()
	case pp.RequestMetadataExtensionMsgType:
		if !t.haveMetadataPiece(piece) {
			c.Post(t.newMetadataExtensionMessage(c, pp.RejectMetadataExtensionMsgType, d["piece"], nil))
			return nil
		}
		start := (1 << 14) * piece
		c.logger.Printf("sending metadata piece %d", piece)
		c.Post(t.newMetadataExtensionMessage(c, pp.DataMetadataExtensionMsgType, piece, t.metadataBytes[start:start+t.metadataPieceSize(piece)]))
		return nil
	case pp.RejectMetadataExtensionMsgType:
		return nil
	default:
		return errors.New("unknown msg_type value")
	}
}

type tlocker struct {
	*torrent
}

func (t tlocker) Lock() {
	t._lock(4)
}

func (t tlocker) Unlock() {
	t._unlock(4)
}
