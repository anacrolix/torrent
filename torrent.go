package torrent

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/netip"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"
	"unsafe"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/chansync"
	"github.com/anacrolix/chansync/events"
	"github.com/anacrolix/dht/v2"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/slices"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/missinggo/v2/pubsub"
	"github.com/anacrolix/multiless"
	"github.com/anacrolix/sync"
	"github.com/pion/datachannel"
	"github.com/sasha-s/go-deadlock"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/internal/check"
	"github.com/anacrolix/torrent/internal/nestedmaps"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	utHolepunch "github.com/anacrolix/torrent/peer_protocol/ut-holepunch"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/tracker"
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
	"github.com/anacrolix/torrent/webseed"
	"github.com/anacrolix/torrent/webtorrent"
	stack2 "github.com/go-stack/stack"
)

func stack(skip int) string {
	return stack2.Trace().TrimBelow(stack2.Caller(skip)).String()
}

type mu struct {
	deadlock.RWMutex
	rlc        atomic.Int32
	lc         atomic.Int32
	rlmu       sync.Mutex
	rlocker    [20]string
	rlocktime  time.Time
	locker     string
	nextlocker string
	locktime   time.Time
}

func (m *mu) RLock() {
	m.RWMutex.RLock()
	m.rlmu.Lock()
	rlc := m.rlc.Load()
	if int(rlc) < len(m.rlocker) {
		m.rlocker[rlc] = string(stack(2))
	}
	if rlc == 0 {
		m.rlocktime = time.Now()
	}
	m.rlc.Add(1)
	m.rlmu.Unlock()
	//fmt.Println("R", m.rlc, string(dbg.Stack())[:40])

}

func (m *mu) RUnlock() {
	m.rlmu.Lock()
	m.rlc.Add(-1)
	rlc := m.rlc.Load()
	if rlc < 0 {
		panic("lock underflow")
	}
	if rlc == 0 {
		m.rlocktime = time.Time{}
	}
	if int(rlc) < len(m.rlocker) {
		m.rlocker[m.rlc.Load()] = ""
	}
	m.rlmu.Unlock()
	m.RWMutex.RUnlock()
	//fmt.Println("RUN", m.rlc) //, string(dbg.Stack()))
}

func (m *mu) Lock() {
	m.rlmu.Lock()
	if m.nextlocker == "" {
		m.nextlocker = string(stack(2))
	}
	m.rlmu.Unlock()
	m.RWMutex.Lock()
	m.lc.Add(1)
	m.rlmu.Lock()
	m.locker = m.nextlocker
	m.locktime = time.Now()
	m.nextlocker = ""
	m.rlmu.Unlock()
}

func (m *mu) Unlock() {
	m.lc.Add(-1)
	if m.lc.Load() < 0 {
		panic("lock underflow")
	}
	m.locker = ""
	m.locktime = time.Time{}
	m.RWMutex.Unlock()
	//fmt.Println("LUN", m.lc) //, string(dbg.Stack()))
}

// Maintains state of torrent within a Client. Many methods should not be called before the info is
// available, see .Info and .GotInfo.
type Torrent struct {
	// Torrent-level aggregate statistics. First in struct to ensure 64-bit
	// alignment. See #262.
	connStats ConnStats
	cl        *Client
	logger    log.Logger

	networkingEnabled      chansync.Flag
	dataDownloadDisallowed chansync.Flag
	dataUploadDisallowed   bool
	userOnWriteChunkErr    func(error)

	closed   chansync.SetOnce
	onClose  []func()
	infoHash metainfo.Hash
	pieces   []Piece

	// The order pieces are requested if there's no stronger reason like availability or priority.
	pieceRequestOrder []int
	// Values are the piece indices that changed.
	pieceStateChanges pubsub.PubSub[PieceStateChange]
	// The size of chunks to request from peers over the wire. This is
	// normally 16KiB by convention these days.
	chunkSize pp.Integer
	chunkPool sync.Pool
	// Total length of the torrent in bytes. Stored because it's not O(1) to
	// get this from the info dict.
	_length g.Option[int64]

	// The storage to open when the info dict becomes available.
	storageOpener *storage.Client
	// Storage for torrent data.
	storage *storage.Torrent
	// Read-locked for using storage, and write-locked for Closing.
	storageLock sync.RWMutex

	// TODO: Only announce stuff is used?
	metainfo metainfo.MetaInfo

	// The info dict. nil if we don't have it (yet).
	info      *metainfo.Info
	fileIndex segments.Index
	files     *[]*File

	_chunksPerRegularPiece chunkIndexType

	webSeeds map[string]*Peer
	// Active peer connections, running message stream loops. TODO: Make this
	// open (not-closed) connections only.
	conns               map[*PeerConn]struct{}
	maxEstablishedConns int
	// Set of addrs to which we're attempting to connect. Connections are
	// half-open until all handshakes are completed.
	halfOpen map[string]map[outgoingConnAttemptKey]*PeerInfo

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known in
	// the swarm.
	peers prioritizedPeers
	// Whether we want to know more peers.
	wantPeersEvent missinggo.Event
	// An announcer for each tracker URL.
	trackerAnnouncers map[string]torrentTrackerAnnouncer
	// How many times we've initiated a DHT announce. TODO: Move into stats.
	numDHTAnnounces int

	// Name used if the info name isn't available. Should be cleared when the
	// Info does become available.
	mu          mu //sync.RWMutex
	displayName string

	// The bencoded bytes of the info dict. This is actively manipulated if
	// the info bytes aren't initially available, and we try to fetch them
	// from peers.
	metadataBytes []byte
	// Each element corresponds to the 16KiB metadata pieces. If true, we have
	// received that piece.
	metadataCompletedChunks []bool
	metadataChanged         sync.Cond

	// Closed when .Info is obtained.
	gotMetainfoC chan struct{}

	readers                map[*reader]struct{}
	_readerNowPieces       bitmap.Bitmap
	_readerReadaheadPieces bitmap.Bitmap

	// A cache of pieces we need to get. Calculated from various piece and
	// file priorities and completion states elsewhere.
	_pendingPieces roaring.Bitmap
	// A cache of completed piece indices.
	_completedPieces roaring.Bitmap
	// Pieces that need to be hashed.
	piecesQueuedForHash       bitmap.Bitmap
	activePieceHashes         atomic.Int64
	hashing                   atomic.Int64
	initialPieceCheckDisabled bool

	connsWithAllPieces map[*Peer]struct{}

	requestState map[RequestIndex]requestState
	// Chunks we've written to since the corresponding piece was last checked.
	dirtyChunks typedRoaring.Bitmap[RequestIndex]

	pex pexState

	// Is On when all pieces are complete.
	Complete chansync.Flag

	// Torrent sources in use keyed by the source string.
	activeSources sync.Map
	sourcesLogger log.Logger

	smartBanCache smartBanCache

	// Large allocations reused between request state updates.
	requestPieceStates []request_strategy.PieceRequestOrderState
	requestIndexes     []RequestIndex

	disableTriggers bool

	// channel for passing hash results to the torrent's hash
	// results processor - this allows piece hashers to run free
	// of the global torrent client lock
	hashResults chan hashResult
	// this is static per torrent it i kept locally to avoid
	// re-
	clientPieceRequestOrder *request_strategy.PieceRequestOrder
}

type outgoingConnAttemptKey = *PeerInfo

func (t *Torrent) length() int64 {
	return t._length.Value
}

func (t *Torrent) selectivePieceAvailabilityFromPeers(i pieceIndex, lock bool) (count int) {
	// This could be done with roaring.BitSliceIndexing.
	for _, peer := range t.peersAsSlice(lock) {
		ok := func() bool {
			if lock {
				t.mu.RLock()
				defer t.mu.RUnlock()
			}
			_, ok := t.connsWithAllPieces[peer]
			return ok
		}

		if ok() {
			return
		}
		if peer.peerHasPiece(i, true, lock) {
			count++
		}
	}
	return
}

func (t *Torrent) decPieceAvailability(i pieceIndex, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if !t.haveInfo(false) {
		return
	}
	p := t.piece(i, false)
	if p.relativeAvailability <= 0 {
		panic(p.relativeAvailability)
	}
	p.relativeAvailability--
	t.updatePieceRequestOrderPiece(i, false)
}

func (t *Torrent) incPieceAvailability(i pieceIndex, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	// If we don't the info, this should be reconciled when we do.
	if t.haveInfo(false) {
		p := t.piece(i, false)
		p.relativeAvailability++
		t.updatePieceRequestOrderPiece(i, false)
	}
}

func (t *Torrent) readerNowPieces() bitmap.Bitmap {
	return t._readerNowPieces
}

func (t *Torrent) readerReadaheadPieces() bitmap.Bitmap {
	return t._readerReadaheadPieces
}

func (t *Torrent) ignorePieceForRequests(i pieceIndex, lock bool) bool {
	return !t.wantPieceIndex(i, lock)
}

// Returns a channel that is closed when the Torrent is closed.
func (t *Torrent) Closed() events.Done {
	return t.closed.Done()
}

// KnownSwarm returns the known subset of the peers in the Torrent's swarm, including active,
// pending, and half-open peers.
func (t *Torrent) KnownSwarm() (ks []PeerInfo) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Add pending peers to the list
	t.peers.Each(func(peer PeerInfo) {
		ks = append(ks, peer)
	})

	// Add half-open peers to the list
	for _, attempts := range t.halfOpen {
		for _, peer := range attempts {
			ks = append(ks, *peer)
		}
	}

	// Add active peers to the list
	for conn := range t.conns {
		ks = append(ks, PeerInfo{
			Id:     conn.PeerID,
			Addr:   conn.RemoteAddr,
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

	return
}

func (t *Torrent) setChunkSize(size pp.Integer) {
	t.chunkSize = size
	t.chunkPool = sync.Pool{
		New: func() interface{} {
			b := make([]byte, size)
			return &b
		},
	}
}

func (t *Torrent) pieceComplete(piece pieceIndex, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return t._completedPieces.Contains(bitmap.BitIndex(piece))
}

func (t *Torrent) pieceCompleteUncached(piece pieceIndex, lock bool) storage.Completion {
	if t.storage == nil {
		return storage.Completion{Complete: false, Ok: true}
	}
	return t.piece(piece, lock).Storage().Completion()
}

// There's a connection to that address already.
func (t *Torrent) addrActive(addr string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if _, ok := t.halfOpen[addr]; ok {
		return true
	}
	for c := range t.conns {
		ra := c.RemoteAddr
		if ra.String() == addr {
			return true
		}
	}
	return false
}

func (t *Torrent) appendUnclosedConns(ret []*PeerConn, lock bool) []*PeerConn {
	return t.appendConns(ret, func(conn *PeerConn) bool {
		return !conn.closed.IsSet()
	}, lock)
}

func (t *Torrent) appendConns(ret []*PeerConn, f func(*PeerConn) bool, lock bool) []*PeerConn {
	conns := t.peerConnsAsSlice(lock)
	defer conns.free()
	for _, c := range conns {
		if f(c) {
			ret = append(ret, c)
		}
	}
	return ret
}

func (t *Torrent) addPeer(p PeerInfo, lock bool) (added bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	cl := t.cl
	torrent.Add(fmt.Sprintf("peers added by source %q", p.Source), 1)
	if t.closed.IsSet() {
		return false
	}
	if ipAddr, ok := tryIpPortFromNetAddr(p.Addr); ok {
		if cl.badPeerIPPort(ipAddr.IP, ipAddr.Port) {
			torrent.Add("peers not added because of bad addr", 1)
			// cl.logger.Printf("peers not added because of bad addr: %v", p)
			return false
		}
	}
	if replaced, ok := t.peers.AddReturningReplacedPeer(p); ok {
		torrent.Add("peers replaced", 1)
		if !replaced.equal(p) {
			t.logger.WithDefaultLevel(log.Debug).Printf("added %v replacing %v", p, replaced)
			added = true
		}
	} else {
		added = true
	}
	t.openNewConns(false)
	for t.peers.Len() > cl.config.TorrentPeersHighWater {
		_, ok := t.peers.DeleteMin()
		if ok {
			torrent.Add("excess reserve peers discarded", 1)
		}
	}
	return
}

func (t *Torrent) invalidateMetadata(lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}
	for i := 0; i < len(t.metadataCompletedChunks); i++ {
		t.metadataCompletedChunks[i] = false
	}
	t.gotMetainfoC = make(chan struct{})
	t.info = nil
}

func (t *Torrent) saveMetadataPiece(index int, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.haveInfo(false) {
		return
	}
	if index >= len(t.metadataCompletedChunks) {
		t.logger.Printf("%s: ignoring metadata piece %d", t, index)
		return
	}
	copy(t.metadataBytes[(1<<14)*index:], data)
	t.metadataCompletedChunks[index] = true
}

func (t *Torrent) metadataPieceCount(lock bool) int {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return (len(t.metadataBytes) + (1 << 14) - 1) / (1 << 14)
}

func (t *Torrent) haveMetadataPiece(piece int, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if t.haveInfo(false) {
		return (1<<14)*piece < len(t.metadataBytes)
	} else {
		return piece < len(t.metadataCompletedChunks) && t.metadataCompletedChunks[piece]
	}
}

func (t *Torrent) metadataSize(lock bool) int {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	return len(t.metadataBytes)
}

func infoPieceHashes(info *metainfo.Info) (ret [][]byte) {
	for i := 0; i < len(info.Pieces); i += sha1.Size {
		ret = append(ret, info.Pieces[i:i+sha1.Size])
	}
	return
}

func (t *Torrent) makePieces() {
	hashes := infoPieceHashes(t.info)
	t.pieces = make([]Piece, len(hashes))
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

func (t *Torrent) cacheLength() {
	var l int64
	for _, f := range t.info.UpvertedFiles() {
		l += f.Length
	}
	t._length = g.Some(l)
}

// TODO: This shouldn't fail for storage reasons. Instead we should handle storage failure
// separately.
func (t *Torrent) setInfo(info *metainfo.Info, lock bool) error {
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
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	t.info = info
	t.displayName = "" // Save a few bytes lol.

	t._chunksPerRegularPiece = chunkIndexType((pp.Integer(t.usualPieceSize()) + t.chunkSize - 1) / t.chunkSize)
	t.updateComplete(false)
	t.fileIndex = segments.NewIndex(common.LengthIterFromUpvertedFiles(info.UpvertedFiles()))
	t.initFiles()
	t.cacheLength()
	t.makePieces()
	return nil
}

func (t *Torrent) pieceRequestOrderKey(i int) request_strategy.PieceRequestOrderKey {
	return request_strategy.PieceRequestOrderKey{
		InfoHash: t.infoHash,
		Index:    i,
	}
}

// This seems to be all the follow-up tasks after info is set, that can't fail.
func (t *Torrent) onSetInfo(lock bool, lockClient bool) {
	if lockClient {
		t.cl.lock()
		defer t.cl.unlock()
	}

	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	t.pieceRequestOrder = rand.Perm(t.numPieces())
	t.initPieceRequestOrder(false)
	g.MakeSliceWithLength(&t.requestPieceStates, t.numPieces())
	for i := range t.pieces {
		p := &t.pieces[i]
		// Need to add relativeAvailability before updating piece completion, as that may result in conns
		// being dropped.
		if p.relativeAvailability != 0 {
			panic(p.relativeAvailability)
		}
		p.relativeAvailability = t.selectivePieceAvailabilityFromPeers(i, false)
		t.addRequestOrderPiece(i, false)
		t.updatePieceCompletion(i, false)

		t.initialPieceCheckDisabled = true

		if !t.initialPieceCheckDisabled && !p.storageCompletionOk {
			// t.logger.Printf("piece %s completion unknown, queueing check", p)
			t.queuePieceCheck(i, false)
		} else {
			// if piece check is not called the piece priority still needs to be set
			t.updatePiecePriority(i, "Torrent.OnSetInfo", false)
		}
	}
	t.cl.event.Broadcast()
	close(t.gotMetainfoC)
	t.updateWantPeersEvent(false)
	t.requestState = make(map[RequestIndex]requestState)
	t.tryCreateMorePieceHashers(false)
	t.iterPeers(func(p *Peer) {
		p.onGotInfo(t.info, true, false)
		p.updateRequests("Torrent.OnSetInfo", true, false)
	}, false)
}

// Called when metadata for a torrent becomes available.
func (t *Torrent) setInfoBytes(b []byte, lock bool, lockClient bool) error {
	if err := func() error {
		if lock {
			t.mu.Lock()
			defer t.mu.Unlock()
		}

		if metainfo.HashBytes(b) != t.infoHash {
			return errors.New("info bytes have wrong hash")
		}
		var info metainfo.Info
		if err := bencode.Unmarshal(b, &info); err != nil {
			return fmt.Errorf("error unmarshalling info bytes: %s", err)
		}
		t.metadataBytes = b
		t.metadataCompletedChunks = nil
		if t.info != nil {
			return nil
		}
		if err := t.setInfo(&info, false); err != nil {
			return err
		}

		return nil
	}(); err != nil {
		return err
	}

	t.onSetInfo(lock, lockClient)
	return nil
}

func (t *Torrent) haveAllMetadataPieces(lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if t.haveInfo(false) {
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
func (t *Torrent) setMetadataSize(size int, lock bool) error {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if t.haveInfo(false) {
		// We already know the correct metadata size.
		return nil
	}
	if uint32(size) > maxMetadataSize {
		return log.WithLevel(log.Warning, errors.New("bad size"))
	}
	if len(t.metadataBytes) == size {
		return nil
	}
	t.metadataBytes = make([]byte, size)
	t.metadataCompletedChunks = make([]bool, (size+(1<<14)-1)/(1<<14))
	t.metadataChanged.Broadcast()

	conns := t.peerConnsAsSlice(false)
	defer conns.free()

	for _, c := range conns {
		c.requestPendingMetadata(false)
	}

	return nil
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *Torrent) name(lock bool) string {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if t.haveInfo(false) {
		return t.info.BestName()
	}

	if t.displayName != "" {
		return t.displayName
	}
	return "infohash:" + t.infoHash.HexString()
}

func (t *Torrent) pieceState(index pieceIndex, lock bool) (ret PieceState) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	p := &t.pieces[index]
	ret.Priority = t.piecePriority(index, false)
	ret.Completion = p.completion(true, false)
	ret.QueuedForHash = p.queuedForHash(false)
	ret.Hashing = p.hashing
	ret.Checking = ret.QueuedForHash || ret.Hashing
	ret.Marking = p.marking
	if !ret.Complete && t.piecePartiallyDownloaded(index, false) {
		ret.Partial = true
	}
	return
}

func (t *Torrent) metadataPieceSize(piece int) int {
	return metadataPieceSize(len(t.metadataBytes), piece)
}

func (t *Torrent) newMetadataExtensionMessage(c *PeerConn, msgType pp.ExtendedMetadataRequestMsgType, piece int, data []byte) pp.Message {
	return pp.Message{
		Type:       pp.Extended,
		ExtendedID: c.PeerExtensionIDs[pp.ExtensionNameMetadata],
		ExtendedPayload: append(bencode.MustMarshal(pp.ExtendedMetadataRequestMsg{
			Piece:     piece,
			TotalSize: len(t.metadataBytes),
			Type:      msgType,
		}), data...),
	}
}

type pieceAvailabilityRun struct {
	Count        pieceIndex
	Availability int
}

func (me pieceAvailabilityRun) String() string {
	return fmt.Sprintf("%v(%v)", me.Count, me.Availability)
}

func (t *Torrent) pieceAvailabilityRuns(lock bool) (ret []pieceAvailabilityRun) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	rle := missinggo.NewRunLengthEncoder(func(el interface{}, count uint64) {
		ret = append(ret, pieceAvailabilityRun{Availability: el.(int), Count: int(count)})
	})
	for i := range t.pieces {
		rle.Append(t.pieces[i].availability(false), 1)
	}
	rle.Flush()
	return
}

func (t *Torrent) pieceAvailabilityFrequencies(lock bool) (freqs []int) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	freqs = make([]int, t.numActivePeers()+1)
	for i := range t.pieces {
		freq := t.pieces[i].availability(false)

		for freq >= len(freqs) {
			freqs = append(freqs, 0)
		}

		freqs[freq]++
	}
	return
}

func (t *Torrent) pieceStateRuns(lock bool) (ret PieceStateRuns) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	rle := missinggo.NewRunLengthEncoder(func(el interface{}, count uint64) {
		ret = append(ret, PieceStateRun{
			PieceState: el.(PieceState),
			Length:     int(count),
		})
	})
	for index := range t.pieces {
		rle.Append(t.pieceState(pieceIndex(index), false), 1)
	}
	rle.Flush()
	return
}

// Produces a small string representing a PieceStateRun.
func (psr PieceStateRun) String() (ret string) {
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
	if psr.Hashing {
		ret += "H"
	}
	if psr.QueuedForHash {
		ret += "Q"
	}
	if psr.Marking {
		ret += "M"
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

func (t *Torrent) writeStatus(w io.Writer) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	fmt.Fprintf(w, "Infohash: %s\n", t.infoHash.HexString())
	fmt.Fprintf(w, "Metadata length: %d\n", t.metadataSize(false))
	if !t.haveInfo(false) {
		fmt.Fprintf(w, "Metadata have: ")
		for _, h := range t.metadataCompletedChunks {
			fmt.Fprintf(w, "%c", func() rune {
				if h {
					return 'H'
				} else {
					return '.'
				}
			}())
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Piece length: %s\n",
		func() string {
			if t.haveInfo(false) {
				return fmt.Sprintf("%v (%v chunks)",
					t.usualPieceSize(),
					float64(t.usualPieceSize())/float64(t.chunkSize))
			} else {
				return "no info"
			}
		}(),
	)
	if t.info != nil {
		fmt.Fprintf(w, "Num Pieces: %d (%d completed)\n", t.numPieces(), t.numPiecesCompleted(false))
		fmt.Fprintf(w, "Piece States: %s\n", t.pieceStateRuns(false))
		// Generates a huge, unhelpful listing when piece availability is very scattered. Prefer
		// availability frequencies instead.
		if false {
			fmt.Fprintf(w, "Piece availability: %v\n", strings.Join(func() (ret []string) {
				for _, run := range t.pieceAvailabilityRuns(false) {
					ret = append(ret, run.String())
				}
				return
			}(), " "))
		}
		fmt.Fprintf(w, "Piece availability frequency: %v\n", strings.Join(
			func() (ret []string) {
				for avail, freq := range t.pieceAvailabilityFrequencies(false) {
					if freq == 0 {
						continue
					}
					ret = append(ret, fmt.Sprintf("%v: %v", avail, freq))
				}
				return
			}(),
			", "))
	}
	fmt.Fprintf(w, "Reader Pieces:")
	t.forReaderOffsetPieces(func(begin, end pieceIndex) (again bool) {
		fmt.Fprintf(w, " %d:%d", begin, end)
		return true
	}, false)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Enabled trackers:\n")
	func() {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "    URL\tExtra\n")
		for _, ta := range slices.Sort(slices.FromMapElems(t.trackerAnnouncers), func(l, r torrentTrackerAnnouncer) bool {
			lu := l.URL()
			ru := r.URL()
			var luns, runs url.URL = *lu, *ru
			luns.Scheme = ""
			runs.Scheme = ""
			var ml missinggo.MultiLess
			ml.StrictNext(luns.String() == runs.String(), luns.String() < runs.String())
			ml.StrictNext(lu.String() == ru.String(), lu.String() < ru.String())
			return ml.Less()
		}).([]torrentTrackerAnnouncer) {
			fmt.Fprintf(tw, "    %q\t%v\n", ta.URL(), ta.statusLine())
		}
		tw.Flush()
	}()

	fmt.Fprintf(w, "DHT Announces: %d\n", t.numDHTAnnounces)

	dumpStats(w, t.stats(false))

	fmt.Fprintf(w, "webseeds:\n")
	t.writePeerStatuses(w, t.webSeedsAsSlice(false), false)

	peerConns := t.peerConnsAsSlice(false)
	defer peerConns.free()

	// Peers without priorities first, then those with. I'm undecided about how to order peers
	// without priorities.
	sort.Slice(peerConns, func(li, ri int) bool {
		l := peerConns[li]
		r := peerConns[ri]
		ml := multiless.New()
		lpp := g.ResultFromTuple(l.peerPriority()).ToOption()
		rpp := g.ResultFromTuple(r.peerPriority()).ToOption()
		ml = ml.Bool(lpp.Ok, rpp.Ok)
		ml = ml.Uint32(rpp.Value, lpp.Value)
		return ml.Less()
	})

	fmt.Fprintf(w, "%v peer conns:\n", len(peerConns))
	t.writePeerStatuses(w, g.SliceMap(peerConns, func(pc *PeerConn) *Peer {
		return &pc.Peer
	}), false)
}

func (t *Torrent) writePeerStatuses(w io.Writer, peers []*Peer, lock bool) {
	var buf bytes.Buffer
	for _, c := range peers {
		fmt.Fprintf(w, "- ")
		buf.Reset()
		c.writeStatus(&buf, true, lock)
		w.Write(bytes.TrimRight(
			bytes.ReplaceAll(buf.Bytes(), []byte("\n"), []byte("\n  ")),
			" "))
	}
}

func (t *Torrent) haveInfo(lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return t.info != nil
}

// Returns a run-time generated MetaInfo that includes the info bytes and
// announce-list as currently known to the client.
func (t *Torrent) newMetaInfo(lock bool) metainfo.MetaInfo {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	return metainfo.MetaInfo{
		CreationDate: time.Now().Unix(),
		Comment:      "dynamic metainfo from client",
		CreatedBy:    "go.torrent",
		AnnounceList: t.metainfo.UpvertedAnnounceList().Clone(),
		InfoBytes: func() []byte {
			if t.haveInfo(false) {
				return t.metadataBytes
			} else {
				return nil
			}
		}(),
		UrlList: func() []string {
			ret := make([]string, 0, len(t.webSeeds))
			for url := range t.webSeeds {
				ret = append(ret, url)
			}
			return ret
		}(),
	}
}

// Returns a count of bytes that are not complete in storage, and not pending being written to
// storage. This value is from the perspective of the download manager, and may not agree with the
// actual state in storage. If you want read data synchronously you should use a Reader. See
// https://github.com/anacrolix/torrent/issues/828.
func (t *Torrent) BytesMissing() (n int64) {
	return t.bytesLeft(true)
}

func (t *Torrent) bytesLeft(lock bool) (left int64) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	roaring.Flip(&t._completedPieces, 0, uint64(t.numPieces())).Iterate(func(x uint32) bool {
		p := t.piece(pieceIndex(x), false)
		left += int64(p.length() - p.numDirtyBytes(false))
		return true
	})
	return
}

// Bytes left to give in tracker announces.
func (t *Torrent) bytesLeftAnnounce(lock bool) int64 {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if t.haveInfo(false) {
		return t.bytesLeft(false)
	} else {
		return -1
	}
}

func (t *Torrent) piecePartiallyDownloaded(piece pieceIndex, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if t.pieceComplete(piece, false) {
		return false
	}
	if t.pieceAllDirty(piece, false) {
		return false
	}
	return t.piece(piece, false).hasDirtyChunks(false)
}

func (t *Torrent) usualPieceSize() int {
	return int(t.info.PieceLength)
}

func (t *Torrent) numPieces() pieceIndex {
	return t.info.NumPieces()
}

func (t *Torrent) numPiecesCompleted(lock bool) (num pieceIndex) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return pieceIndex(t._completedPieces.GetCardinality())
}

func (t *Torrent) close(wg *sync.WaitGroup) (err error) {
	if !t.closed.Set() {
		err = errors.New("already closed")
		return
	}
	for _, f := range t.onClose {
		f()
	}
	if t.storage != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.storageLock.Lock()
			defer t.storageLock.Unlock()
			if f := t.storage.Close; f != nil {
				err1 := f()
				if err1 != nil {
					t.logger.WithDefaultLevel(log.Warning).Printf("error closing storage: %v", err1)
				}
			}
		}()
	}
	t.iterPeers(func(p *Peer) {
		p.close(true, true)
	}, true)
	if t.storage != nil {
		t.deletePieceRequestOrder()
	}
	t.assertAllPiecesRelativeAvailabilityZero(true)
	t.pex.Reset()
	t.cl.event.Broadcast()
	t.pieceStateChanges.Close()
	t.updateWantPeersEvent(true)
	return
}

func (t *Torrent) assertAllPiecesRelativeAvailabilityZero(lock bool) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	for i := range t.pieces {
		p := &t.pieces[i]
		if p.relativeAvailability != 0 {
			panic(fmt.Sprintf("piece %v has relative availability %v", i, p.relativeAvailability))
		}
	}
}

func (t *Torrent) requestOffset(r Request) int64 {
	return torrentRequestOffset(t.length(), int64(t.usualPieceSize()), r)
}

// Return the request that would include the given offset into the torrent data. Returns !ok if
// there is no such request.
func (t *Torrent) offsetRequest(off int64) (req Request, ok bool) {
	return torrentOffsetRequest(t.length(), t.info.PieceLength, int64(t.chunkSize), off)
}

func (t *Torrent) writeChunk(piece int, begin int64, data []byte, lock bool) (err error) {
	//defer perf.ScopeTimerErr(&err)()
	n, err := t.piece(piece, lock).Storage().WriteAt(data, begin)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func (t *Torrent) bitfield(lock bool) (bf []bool) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	bf = make([]bool, t.numPieces())
	t._completedPieces.Iterate(func(piece uint32) (again bool) {
		bf[piece] = true
		return true
	})
	return
}

func (t *Torrent) pieceNumChunks(piece pieceIndex) chunkIndexType {
	return chunkIndexType((t.pieceLength(piece) + t.chunkSize - 1) / t.chunkSize)
}

func (t *Torrent) chunksPerRegularPiece(lock bool) chunkIndexType {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	return t._chunksPerRegularPiece
}

func (t *Torrent) pendAllChunkSpecs(pieceIndex pieceIndex, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	t.dirtyChunks.RemoveRange(
		uint64(t.pieceRequestIndexOffset(pieceIndex, false)),
		uint64(t.pieceRequestIndexOffset(pieceIndex+1, false)))
}

func (t *Torrent) pieceLength(piece pieceIndex) pp.Integer {
	if t.info.PieceLength == 0 {
		// There will be no variance amongst pieces. Only pain.
		return 0
	}
	if piece == t.numPieces()-1 {
		ret := pp.Integer(t.length() % t.info.PieceLength)
		if ret != 0 {
			return ret
		}
	}
	return pp.Integer(t.info.PieceLength)
}

func (t *Torrent) smartBanBlockCheckingWriter(piece pieceIndex, lock bool) *blockCheckingWriter {
	return &blockCheckingWriter{
		cache:        &t.smartBanCache,
		requestIndex: t.pieceRequestIndexOffset(piece, lock),
		chunkSize:    t.chunkSize.Int(),
	}
}

func (t *Torrent) hashPiece(p *Piece) (
	ret metainfo.Hash,
	// These are peers that sent us blocks that differ from what we hash here.
	differingPeers map[bannableAddr]struct{},
	err error,
) {
	p.waitNoPendingWrites()
	storagePiece := p.Storage()

	// Does the backend want to do its own hashing?
	if i, ok := storagePiece.PieceImpl.(storage.SelfHashing); ok {
		var sum metainfo.Hash
		// log.Printf("A piece decided to self-hash: %d", piece)
		sum, err = i.SelfHash()
		missinggo.CopyExact(&ret, sum)
		return
	}

	hash := pieceHash.New()
	const logPieceContents = false
	smartBanWriter := t.smartBanBlockCheckingWriter(p.index, true)
	writers := []io.Writer{hash, smartBanWriter}
	var examineBuf bytes.Buffer
	if logPieceContents {
		writers = append(writers, &examineBuf)
	}
	_, err = storagePiece.WriteTo(io.MultiWriter(writers...))
	if logPieceContents {
		t.logger.WithDefaultLevel(log.Debug).Printf("hashed %q with copy err %v", examineBuf.Bytes(), err)
	}
	smartBanWriter.Flush()
	differingPeers = smartBanWriter.badPeers
	missinggo.CopyExact(&ret, hash.Sum(nil))
	return
}

func (t *Torrent) haveAnyPieces(lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return !t._completedPieces.IsEmpty()
}

func (t *Torrent) haveAllPieces(lock bool) bool {
	if !t.haveInfo(lock) {
		return false
	}
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return t._completedPieces.GetCardinality() == bitmap.BitRange(t.numPieces())
}

func (t *Torrent) havePiece(index pieceIndex, lock bool) bool {
	return t.haveInfo(lock) && t.pieceComplete(index, lock)
}

func (t *Torrent) maybeDropMutuallyCompletePeer(
	// I'm not sure about taking peer here, not all peer implementations actually drop. Maybe that's
	// okay?
	p *PeerConn,
	lock bool,
	lockPeer bool,
) {
	if !t.cl.config.DropMutuallyCompletePeers {
		return
	}
	if !t.haveAllPieces(lock) {
		return
	}
	if all, known := p.peerHasAllPieces(lockPeer, lock); !(known && all) {
		return
	}
	if p.useful(lockPeer, lock) {
		return
	}
	p.logger.Levelf(log.Debug, "is mutually complete; dropping")
	p.drop(lockPeer, lock)
}

func (t *Torrent) haveChunk(r Request, lock bool) (ret bool) {
	// defer func() {
	// 	log.Println("have chunk", r, ret)
	// }()
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if !t.haveInfo(false) {
		return false
	}
	if t.pieceComplete(pieceIndex(r.Index), false) {
		return true
	}

	return !t.piece(int(r.Index), false).pendingChunk(r.ChunkSpec, t.chunkSize, false)
}

func chunkIndexFromChunkSpec(cs ChunkSpec, chunkSize pp.Integer) chunkIndexType {
	return chunkIndexType(cs.Begin / chunkSize)
}

func (t *Torrent) wantPieceIndex(index pieceIndex, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return !t._pendingPieces.IsEmpty() && t._pendingPieces.Contains(uint32(index))
}

// A pool of []*PeerConn, to reduce allocations in functions that need to index or sort Torrent
// conns (which is a map).
var peerConnSlices sync.Pool

type conns []*PeerConn

func (c conns) numOutgoingConns() (ret int) {
	for _, con := range c {
		if con.outgoing {
			ret++
		}
	}
	return
}

func (c conns) numReceivedConns() (ret int) {
	for _, con := range c {
		if con.Discovery == PeerSourceIncoming {
			ret++
		}
	}
	return
}

func (c conns) free() {
	peerConnSlices.Put(c)
}

func (t *Torrent) webSeedsAsSlice(lock bool) (ret []*Peer) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	ret = make([]*Peer, 0, len(t.webSeeds))
	for _, ws := range t.webSeeds {
		ret = append(ret, ws)
	}
	return ret
}

func (t *Torrent) peerConnsAsSlice(lock bool) conns {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	conns := getPeerConnSlice(len(t.conns))
	for k := range t.conns {
		conns = append(conns, k)
	}

	return conns
}

func getPeerConnSlice(cap int) conns {
	getInterface := peerConnSlices.Get()
	if getInterface == nil {
		return make(conns, 0, cap)
	} else {
		return getInterface.(conns)[:0]
	}
}

// Calls the given function with a slice of unclosed conns. It uses a pool to reduce allocations as
// this is a frequent occurrence.
func (t *Torrent) withUnclosedConns(f func([]*PeerConn), lock bool) {
	conns := func() conns {
		if lock {
			t.mu.RLock()
			defer t.mu.RUnlock()
		}

		return getPeerConnSlice(len(t.conns))
	}()

	sl := t.appendUnclosedConns(conns, lock)
	f(sl)
	conns.free()
}

func (t *Torrent) worstBadConnFromSlice(opts worseConnLensOpts, sl []*PeerConn) *PeerConn {
	wcs := worseConnSlice{conns: sl}
	wcs.initKeys(opts)
	heap.Init(&wcs)
	for wcs.Len() != 0 {
		if c := func(c *PeerConn) *PeerConn {
			c.mu.RLock()
			defer c.mu.RUnlock()
			if opts.incomingIsBad && !c.outgoing {
				return c
			}
			if opts.outgoingIsBad && c.outgoing {
				return c
			}
			if c._stats.ChunksReadWasted.Int64() >= 6 && c._stats.ChunksReadWasted.Int64() > c._stats.ChunksReadUseful.Int64() {
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

			return nil
		}(heap.Pop(&wcs).(*PeerConn)); c == nil {
			return c
		}
	}
	return nil
}

// The worst connection is one that hasn't been sent, or sent anything useful for the longest. A bad
// connection is one that usually sends us unwanted pieces, or has been in the worse half of the
// established connections for more than a minute. This is O(n log n). If there was a way to not
// consider the position of a conn relative to the total number, it could be reduced to O(n).
func (t *Torrent) worstBadConn(opts worseConnLensOpts, lock bool) (ret *PeerConn) {
	opts.lockTorrent = lock
	t.withUnclosedConns(func(ucs []*PeerConn) {
		ret = t.worstBadConnFromSlice(opts, ucs)
	}, lock)
	return
}

type PieceStateChange struct {
	Index int
	PieceState
}

func (t *Torrent) publishPieceStateChange(piece pieceIndex, lock bool) {
	var publisher *pubsub.PubSub[PieceStateChange]
	var cur PieceState

	defer func() {
		if publisher != nil {
			publisher.Publish(PieceStateChange{
				int(piece),
				cur,
			})

		}
	}()

	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	cur = t.pieceState(piece, false)
	p := t.piece(piece, false)
	p.mu.Lock()
	if cur != p.publicPieceState {
		p.publicPieceState = cur
		publisher = &t.pieceStateChanges
	}
	p.mu.Unlock()
}

func (t *Torrent) pieceNumPendingChunks(piece pieceIndex, lock bool) pp.Integer {
	if t.pieceComplete(piece, lock) {
		return 0
	}

	p := t.piece(piece, lock)
	return pp.Integer(t.pieceNumChunks(piece) - p.numDirtyChunks(lock))
}

func (t *Torrent) pieceAllDirty(piece pieceIndex, lock bool) bool {
	return t.piece(piece, lock).allChunksDirty(lock)
}

func (t *Torrent) readersChanged(lock bool) {
	t.updateReaderPieces(lock)
	t.updateAllPiecePriorities("Torrent.readersChanged", lock)
}

func (t *Torrent) updateReaderPieces(lock bool) {
	t._readerNowPieces, t._readerReadaheadPieces = t.readerPiecePriorities(lock)
}

func (t *Torrent) readerPosChanged(from, to pieceRange, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if from == to {
		return
	}
	t.updateReaderPieces(false)
	// Order the ranges, high and low.
	l, h := from, to
	if l.begin > h.begin {
		l, h = h, l
	}
	if l.end < h.begin {
		// Two distinct ranges.
		t.updatePiecePriorities(l.begin, l.end, "Torrent.readerPosChanged", false)
		t.updatePiecePriorities(h.begin, h.end, "Torrent.readerPosChanged", false)
	} else {
		// Ranges overlap.
		end := l.end
		if h.end > end {
			end = h.end
		}
		t.updatePiecePriorities(l.begin, end, "Torrent.readerPosChanged", false)
	}
}

func (t *Torrent) maybeNewConns(lock bool) {
	// Tickle the accept routine.
	t.cl.event.Broadcast()
	t.openNewConns(lock)
}

func (t *Torrent) onPiecePendingTriggers(piece pieceIndex, reason string, lock bool) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	containsPiece := t._pendingPieces.Contains(uint32(piece))
	if containsPiece {
		t.iterPeers(func(c *Peer) {
			// if c.requestState.Interested {
			// 	return
			// }
			if !c.isLowOnRequests(true, false) {
				return
			}
			if !c.peerHasPiece(piece, true, false) {
				return
			}
			if ignore := c.requestState.Interested && c.peerChoking && !c.peerAllowedFast.Contains(piece); ignore {
				return
			}
			c.updateRequests(reason, true, false)
		}, false)
	}
	t.maybeNewConns(false)
	t.publishPieceStateChange(piece, false)
}

func (t *Torrent) updatePiecePriorityNoTriggers(piece pieceIndex, lock bool) (pendingChanged bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	p := t.piece(piece, false)

	newPrio := p.uncachedPriority(false)
	// t.logger.Printf("torrent %p: piece %d: uncached priority: %v", t, piece, newPrio)
	if newPrio == PiecePriorityNone {
		pendingChanged = t._pendingPieces.CheckedRemove(uint32(piece))
	} else {
		pendingChanged = t._pendingPieces.CheckedAdd(uint32(piece))
	}
	// this needs to happen after t._pendingPieces is set otherwise the
	// updated pending state is ignored
	if pendingChanged && !t.closed.IsSet() {
		// It would be possible to filter on pure-priority changes here to avoid churning the piece
		// request order.
		t.updatePieceRequestOrderPiece(piece, false)
	}

	return pendingChanged
}

func (t *Torrent) updatePiecePriority(piece pieceIndex, reason string, lock bool) {
	if t.updatePiecePriorityNoTriggers(piece, lock) && !t.disableTriggers {
		t.onPiecePendingTriggers(piece, reason, lock)
	}
	t.updatePieceRequestOrderPiece(piece, lock)
}

func (t *Torrent) updateAllPiecePriorities(reason string, lock bool) {
	t.updatePiecePriorities(0, t.numPieces(), reason, lock)
}

// Update all piece priorities in one hit. This function should have the same
// output as updatePiecePriority, but across all pieces.
func (t *Torrent) updatePiecePriorities(begin, end pieceIndex, reason string, lock bool) {
	for i := begin; i < end; i++ {
		t.updatePiecePriority(i, reason, lock)
	}
}

// Returns the range of pieces [begin, end) that contains the extent of bytes.
func (t *Torrent) byteRegionPieces(off, size int64) (begin, end pieceIndex) {
	if off >= t.length() {
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

// Returns true if all iterations complete without breaking. Returns the read regions for all
// readers. The reader regions should not be merged as some callers depend on this method to
// enumerate readers.
func (t *Torrent) forReaderOffsetPieces(f func(begin, end pieceIndex) (more bool), lock bool) (all bool) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

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

func (t *Torrent) piecePriority(piece pieceIndex, lock bool) piecePriority {
	return t.piece(piece, lock).uncachedPriority(lock)
}

func (t *Torrent) pendRequest(req RequestIndex) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.piece(t.pieceIndexOfRequestIndex(req, false), false).pendChunkIndex(req%t.chunksPerRegularPiece(false), false)
}

func (t *Torrent) pieceCompletionChanged(piece pieceIndex, reason string, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	t.cl.event.Broadcast()
	if t.pieceComplete(piece, false) {
		t.onPieceCompleted(piece, false)
	} else {
		t.onIncompletePiece(piece, false)
	}
	t.updatePiecePriority(piece, reason, false)
}

func (t *Torrent) maxHalfOpen(lock bool) int {
	// Note that if we somehow exceed the maximum established conns, we want
	// the negative value to have an effect.

	conns := t.peerConnsAsSlice(lock)
	defer conns.free()

	establishedHeadroom := int64(t.maxEstablishedConns - len(conns))
	extraIncoming := int64(conns.numReceivedConns() - t.maxEstablishedConns/2)
	// We want to allow some experimentation with new peers, and to try to
	// upset an oversupply of received connections.
	return int(min(
		max(5, extraIncoming)+establishedHeadroom,
		int64(t.cl.config.HalfOpenConnsPerTorrent),
	))
}

func (t *Torrent) openNewConns(lock bool) (initiated int) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	defer t.updateWantPeersEvent(false)

	for t.peers.Len() != 0 {
		if !t.wantOutgoingConns(false) {
			return
		}
		if len(t.halfOpen) >= t.maxHalfOpen(false) {
			return
		}
		if len(t.cl.dialers) == 0 {
			return
		}
		if t.cl.numHalfOpen >= t.cl.config.TotalHalfOpenConns {
			return
		}
		p := t.peers.PopMax()
		opts := outgoingConnOpts{
			peerInfo:                 p,
			t:                        t,
			requireRendezvous:        false,
			skipHolepunchRendezvous:  false,
			receivedHolepunchConnect: false,
			HeaderObfuscationPolicy:  t.cl.config.HeaderObfuscationPolicy,
		}
		initiateConn(opts, false, false)
		initiated++
	}

	return
}

func (t *Torrent) updatePieceCompletion(piece pieceIndex, lock bool) bool {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	p := t.piece(piece, false)

	p.mu.Lock()

	uncached := t.pieceCompleteUncached(piece, false)
	cached := p.completion(false, false)

	changed := cached != uncached
	complete := uncached.Complete
	p.storageCompletionOk = uncached.Ok
	lenDirtiers := len(p.dirtiers)

	p.mu.Unlock()

	x := uint32(piece)

	if complete {
		t._completedPieces.Add(x)
		t.openNewConns(false)
	} else {
		t._completedPieces.Remove(x)
	}
	t.updatePieceRequestOrderPiece(piece, false)
	t.updateComplete(false)

	if complete && lenDirtiers != 0 {
		t.logger.Printf("marked piece %v complete but still has dirtiers", piece)
	}
	if changed {
		//slog.Debug(
		//	"piece completion changed",
		//	slog.Int("piece", piece),
		//	slog.Any("from", cached),
		//	slog.Any("to", uncached))
		t.pieceCompletionChanged(piece, "Torrent.updatePieceCompletion", false)
	}
	return changed
}

// Non-blocking read. Client lock is not required.
func (t *Torrent) readAt(b []byte, off int64, lock bool) (n int, err error) {
	for len(b) != 0 {
		p := t.piece(int(off/t.info.PieceLength), lock)

		p.waitNoPendingWrites()
		var n1 int
		n1, err = p.Storage().ReadAt(b, off-p.Info().Offset())
		if n1 == 0 {
			break
		}
		off += int64(n1)
		n += n1
		b = b[n1:]
	}
	return
}

// Returns an error if the metadata was completed, but couldn't be set for some reason. Blame it on
// the last peer to contribute. TODO: Actually we shouldn't blame peers for failure to open storage
// etc. Also we should probably cached metadata pieces per-Peer, to isolate failure appropriately.
func (t *Torrent) maybeCompleteMetadata() error {
	t.cl.lock()
	defer t.cl.unlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.haveInfo(false) {
		// Nothing to do.
		return nil
	}
	if !t.haveAllMetadataPieces(false) {
		// Don't have enough metadata pieces.
		return nil
	}
	err := t.setInfoBytes(t.metadataBytes, false, false)
	if err != nil {
		t.invalidateMetadata(false)
		return fmt.Errorf("error setting info bytes: %s", err)
	}
	if t.cl.config.Debug {
		t.logger.Printf("%s: got metadata from peers", t)
	}
	return nil
}

func (t *Torrent) readerPiecePriorities(lock bool) (now, readahead bitmap.Bitmap) {
	t.forReaderOffsetPieces(func(begin, end pieceIndex) bool {
		if end > begin {
			now.Add(bitmap.BitIndex(begin))
			readahead.AddRange(bitmap.BitRange(begin)+1, bitmap.BitRange(end))
		}
		return true
	}, lock)
	return
}

func (t *Torrent) needData(lock bool) bool {
	if t.closed.IsSet() {
		return false
	}
	if !t.haveInfo(lock) {
		return true
	}
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return !t._pendingPieces.IsEmpty()
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

func (t *Torrent) addTrackers(announceList [][]string, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	fullAnnounceList := &t.metainfo.AnnounceList
	t.metainfo.AnnounceList = appendMissingTrackerTiers(*fullAnnounceList, len(announceList))
	for tierIndex, trackerURLs := range announceList {
		(*fullAnnounceList)[tierIndex] = appendMissingStrings((*fullAnnounceList)[tierIndex], trackerURLs)
	}
	t.startMissingTrackerScrapers()
	t.updateWantPeersEvent(false)
}

// Don't call this before the info is available.
func (t *Torrent) bytesCompleted() int64 {
	if !t.haveInfo(true) {
		return 0
	}
	return t.length() - t.bytesLeft(true)
}

func (t *Torrent) SetInfoBytes(b []byte) (err error) {
	return t.setInfoBytes(b, true, true)
}

// Returns true if connection is removed from torrent.Conns.
func (t *Torrent) deletePeerConn(c *PeerConn, lock bool, lockPeer bool) (ret bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if !c.closed.IsSet() {
		panic("connection is not closed")
		// There are behaviours prevented by the closed state that will fail
		// if the connection has been deleted.
	}
	_, ret = t.conns[c]
	delete(t.conns, c)
	// Avoid adding a drop event more than once. Probably we should track whether we've generated
	// the drop event against the PexConnState instead.
	if ret {
		if !t.cl.config.DisablePEX {
			func() {
				if lockPeer {
					c.mu.Lock()
					defer c.mu.Unlock()
				}
				t.pex.Drop(c)
			}()
		}
	}
	torrent.Add("deleted connections", 1)
	c.deleteAllRequests("Torrent.deletePeerConn", lockPeer, false)
	t.assertPendingRequests(false)
	if t.numActivePeers() == 0 && len(t.connsWithAllPieces) != 0 {
		panic(fmt.Sprintf("no active peers, but %d conns with all", len(t.connsWithAllPieces)))
	}
	return
}

func (t *Torrent) decPeerPieceAvailability(p *Peer, lock bool, lockPeer bool) {
	if t.deleteConnWithAllPieces(p, lock) {
		return
	}
	if !t.haveInfo(lock) {
		return
	}

	if lockPeer {
		p.mu.RLock()
		defer p.mu.RUnlock()
	}

	p.peerPieces(false).Iterate(func(i uint32) bool {
		t.decPieceAvailability(pieceIndex(i), lock)
		return true
	})
}

func (t *Torrent) assertPendingRequests(lock bool) {
	if !check.Enabled {
		return
	}
	// var actual pendingRequests
	// if t.haveInfo() {
	// 	actual.m = make([]int, t.numChunks())
	// }
	// t.iterPeers(func(p *Peer) {
	// 	p.requestState.Requests.Iterate(func(x uint32) bool {
	// 		actual.Inc(x)
	// 		return true
	// 	})
	// })
	// diff := cmp.Diff(actual.m, t.pendingRequests.m)
	// if diff != "" {
	// 	panic(diff)
	// }
}

func (t *Torrent) dropConnection(c *PeerConn, lock bool, lockPeer bool) {
	t.cl.event.Broadcast()
	c.close(lockPeer, lock)
	if t.deletePeerConn(c, lock, lockPeer) {
		t.openNewConns(lock)
	}
}

// Peers as in contact information for dialing out.
func (t *Torrent) wantPeers(lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if t.closed.IsSet() {
		return false
	}
	if t.peers.Len() > t.cl.config.TorrentPeersLowWater {
		return false
	}
	return t.wantOutgoingConns(false)
}

func (t *Torrent) updateWantPeersEvent(lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if t.wantPeers(false) {
		t.wantPeersEvent.Set()
	} else {
		t.wantPeersEvent.Clear()
	}
}

// Returns whether the client should make effort to seed the torrent.
func (t *Torrent) seeding(lock bool) bool {
	cl := t.cl
	if t.closed.IsSet() {
		return false
	}
	if t.dataUploadDisallowed {
		return false
	}
	if cl.config.NoUpload {
		return false
	}
	if !cl.config.Seed {
		return false
	}
	if cl.config.DisableAggressiveUpload && t.needData(lock) {
		return false
	}
	return true
}

func (t *Torrent) onWebRtcConn(
	c datachannel.ReadWriteCloser,
	dcc webtorrent.DataChannelContext,
) {
	defer c.Close()
	netConn := webrtcNetConn{
		ReadWriteCloser:    c,
		DataChannelContext: dcc,
	}
	peerRemoteAddr := netConn.RemoteAddr()
	//t.logger.Levelf(log.Critical, "onWebRtcConn remote addr: %v", peerRemoteAddr)
	if t.cl.badPeerAddr(peerRemoteAddr) {
		return
	}
	localAddrIpPort := missinggo.IpPortFromNetAddr(netConn.LocalAddr())
	pc, err := t.cl.initiateProtocolHandshakes(
		context.Background(),
		netConn,
		t,
		false,
		newConnectionOpts{
			outgoing:        dcc.LocalOffered,
			remoteAddr:      peerRemoteAddr,
			localPublicAddr: localAddrIpPort,
			network:         webrtcNetwork,
			connString:      fmt.Sprintf("webrtc offer_id %x: %v", dcc.OfferId, regularNetConnPeerConnConnString(netConn)),
		},
	)
	if err != nil {
		t.logger.WithDefaultLevel(log.Error).Printf("error in handshaking webrtc connection: %v", err)
		return
	}
	if dcc.LocalOffered {
		pc.Discovery = PeerSourceTracker
	} else {
		pc.Discovery = PeerSourceIncoming
	}
	pc.conn.SetWriteDeadline(time.Time{})
	err = t.runHandshookConn(pc, true)
	if err != nil {
		t.logger.WithDefaultLevel(log.Debug).Printf("error running handshook webrtc conn: %v", err)
	}
}

func (t *Torrent) logRunHandshookConn(pc *PeerConn, logAll bool, level log.Level, lockClient bool) {
	err := t.runHandshookConn(pc, lockClient)
	if err != nil || logAll {
		t.logger.WithDefaultLevel(level).Levelf(log.ErrorLevel(err), "error running handshook conn: %v", err)
	}
}

func (t *Torrent) runHandshookConnLoggingErr(pc *PeerConn, lockClient bool) {
	t.logRunHandshookConn(pc, false, log.Debug, lockClient)
}

func (t *Torrent) startWebsocketAnnouncer(u url.URL) torrentTrackerAnnouncer {
	wtc, release := t.cl.websocketTrackers.Get(u.String(), t.infoHash)
	// This needs to run before the Torrent is dropped from the Client, to prevent a new webtorrent.TrackerClient for
	// the same info hash before the old one is cleaned up.
	t.onClose = append(t.onClose, release)
	wst := websocketTrackerStatus{u, wtc}
	go func() {
		err := wtc.Announce(tracker.Started, t.infoHash)
		if err != nil {
			t.logger.WithDefaultLevel(log.Warning).Printf(
				"error in initial announce to %q: %v",
				u.String(), err,
			)
		}
	}()
	return wst
}

func (t *Torrent) startScrapingTracker(_url string) {
	if _url == "" {
		return
	}
	u, err := url.Parse(_url)
	if err != nil {
		// URLs with a leading '*' appear to be a uTorrent convention to disable trackers.
		if _url[0] != '*' {
			t.logger.Levelf(log.Warning, "error parsing tracker url: %v", err)
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
	if _, ok := t.trackerAnnouncers[_url]; ok {
		return
	}
	sl := func() torrentTrackerAnnouncer {
		switch u.Scheme {
		case "ws", "wss":
			if t.cl.config.DisableWebtorrent {
				return nil
			}
			return t.startWebsocketAnnouncer(*u)
		case "udp4":
			if t.cl.config.DisableIPv4Peers || t.cl.config.DisableIPv4 {
				return nil
			}
		case "udp6":
			if t.cl.config.DisableIPv6 {
				return nil
			}
		}
		newAnnouncer := &trackerScraper{
			u:               *u,
			t:               t,
			lookupTrackerIp: t.cl.config.LookupTrackerIp,
		}
		go newAnnouncer.Run()
		return newAnnouncer
	}()
	if sl == nil {
		return
	}
	if t.trackerAnnouncers == nil {
		t.trackerAnnouncers = make(map[string]torrentTrackerAnnouncer)
	}
	t.trackerAnnouncers[_url] = sl
}

// Adds and starts tracker scrapers for tracker URLs that aren't already
// running.
func (t *Torrent) startMissingTrackerScrapers() {
	if t.cl.config.DisableTrackers {
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
func (t *Torrent) announceRequest(event tracker.AnnounceEvent, lock bool, lockClient bool) tracker.AnnounceRequest {
	numWant := func() int32 {
		dialers := func() int {
			if lockClient {
				t.cl.rLock()
				defer t.cl.rUnlock()
			}
			return len(t.cl.dialers)
		}()

		if t.wantPeers(lock) && dialers > 0 {
			return 200 // Win has UDP packet limit. See: https://github.com/anacrolix/torrent/issues/764
		} else {
			return 0
		}
	}()

	bytesLeft := t.bytesLeftAnnounce(lock)

	// Note that IPAddress is not set. It's set for UDP inside the tracker code, since it's
	// dependent on the network in use.
	if lockClient {
		t.cl.rLock()
		defer t.cl.rUnlock()
	}

	return tracker.AnnounceRequest{
		Event:    event,
		NumWant:  numWant,
		Port:     uint16(t.cl.incomingPeerPort()),
		PeerId:   t.cl.peerID,
		InfoHash: t.infoHash,
		Key:      t.cl.announceKey(),

		// The following are vaguely described in BEP 3.

		Left:     bytesLeft,
		Uploaded: t.connStats.BytesWrittenData.Int64(),
		// There's no mention of wasted or unwanted download in the BEP.
		Downloaded: t.connStats.BytesReadUsefulData.Int64(),
	}
}

// Adds peers revealed in an announce until the announce ends, or we have
// enough peers.
func (t *Torrent) consumeDhtAnnouncePeers(pvs <-chan dht.PeersValues) {
	for v := range pvs {
		t.mu.Lock()
		added := 0
		for _, cp := range v.Peers {
			if cp.Port == 0 {
				// Can't do anything with this.
				continue
			}
			if t.addPeer(PeerInfo{
				Addr:   ipPortAddr{cp.IP, cp.Port},
				Source: PeerSourceDhtGetPeers,
			}, false) {
				added++
			}
		}
		t.mu.Unlock()
		// if added != 0 {
		// 	log.Printf("added %v peers from dht for %v", added, t.InfoHash().HexString())
		// }
	}
}

// Announce using the provided DHT server. Peers are consumed automatically. done is closed when the
// announce ends. stop will force the announce to end.
func (t *Torrent) AnnounceToDht(s DhtServer) (done <-chan struct{}, stop func(), err error) {
	ps, err := s.Announce(t.infoHash, t.cl.incomingPeerPort(), true)
	if err != nil {
		return
	}
	_done := make(chan struct{})
	done = _done
	stop = ps.Close
	go func() {
		t.consumeDhtAnnouncePeers(ps.Peers())
		close(_done)
	}()
	return
}

func (t *Torrent) timeboxedAnnounceToDht(s DhtServer) error {
	_, stop, err := t.AnnounceToDht(s)
	if err != nil {
		return err
	}
	select {
	case <-t.closed.Done():
	case <-time.After(5 * time.Minute):
	}
	stop()
	return nil
}

func (t *Torrent) dhtAnnouncer(s DhtServer) {
	cl := t.cl
	cl.lock()
	defer cl.unlock()
	for {
		for {
			if t.closed.IsSet() {
				return
			}
			// We're also announcing ourselves as a listener, so we don't just want peer addresses.
			// TODO: We can include the announce_peer step depending on whether we can receive
			// inbound connections. We should probably only announce once every 15 mins too.
			if !t.wantAnyConns(true) {
				goto wait
			}
			// TODO: Determine if there's a listener on the port we're announcing.
			if len(cl.dialers) == 0 && len(cl.listeners) == 0 {
				goto wait
			}
			break
		wait:
			cl.event.Wait()
		}
		func() {
			t.numDHTAnnounces++
			cl.unlock()
			defer cl.lock()
			err := t.timeboxedAnnounceToDht(s)
			if err != nil {
				t.logger.WithDefaultLevel(log.Warning).Printf("error announcing %q to DHT: %s", t, err)
			}
		}()
	}
}

func (t *Torrent) addPeers(peers []PeerInfo, lock bool) (added int) {
	for _, p := range peers {
		if t.addPeer(p, lock) {
			added++
		}
	}
	return
}

// The returned TorrentStats may require alignment in memory. See
// https://github.com/anacrolix/torrent/issues/383.
func (t *Torrent) Stats() TorrentStats {
	return t.stats(true)
}

func (t *Torrent) stats(lock bool) (ret TorrentStats) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	ret.ActivePeers = len(t.conns)
	ret.HalfOpenPeers = len(t.halfOpen)
	ret.PendingPeers = t.peers.Len()
	ret.TotalPeers = t.numTotalPeers(false)
	ret.ConnectedSeeders = 0
	for c := range t.conns {
		if all, ok := c.peerHasAllPieces(true, false); all && ok {
			ret.ConnectedSeeders++
		}
	}
	ret.ConnStats = t.connStats.Copy()
	ret.PiecesComplete = t.numPiecesCompleted(false)
	return
}

// The total number of peers in the torrent.
func (t *Torrent) numTotalPeers(lock bool) int {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	peers := make(map[string]struct{})
	for conn := range t.conns {
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
	t.peers.Each(func(peer PeerInfo) {
		peers[peer.Addr.String()] = struct{}{}
	})
	return len(peers)
}

// Reconcile bytes transferred before connection was associated with a
// torrent.
func (t *Torrent) reconcileHandshakeStats(c *Peer) {
	if c._stats != (ConnStats{
		// Handshakes should only increment these fields:
		BytesWritten: c._stats.BytesWritten,
		BytesRead:    c._stats.BytesRead,
	}) {
		panic("bad stats")
	}
	c.postHandshakeStats(func(cs *ConnStats) {
		cs.BytesRead.Add(c._stats.BytesRead.Int64())
		cs.BytesWritten.Add(c._stats.BytesWritten.Int64())
	})
	c.reconciledHandshakeStats = true
}

// Returns true if the connection is added.
func (t *Torrent) addPeerConn(c *PeerConn, lockTorrent bool) (err error) {
	defer func() {
		if err == nil {
			torrent.Add("added connections", 1)
		}
	}()

	if t.closed.IsSet() {
		return errors.New("torrent closed")
	}

	if lockTorrent {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	conns := t.peerConnsAsSlice(false)
	defer conns.free()

	for _, c0 := range conns {
		if c.PeerID != c0.PeerID {
			continue
		}
		if !t.cl.config.DropDuplicatePeerIds {
			continue
		}
		if c.hasPreferredNetworkOver(c0) {
			c0.close(true, false)
			t.deletePeerConn(c0, false, true)
		} else {
			return errors.New("existing connection preferred")
		}
	}

	if len(conns) >= t.maxEstablishedConns {
		numOutgoing := conns.numOutgoingConns()
		numIncoming := len(conns) - numOutgoing
		c := t.worstBadConn(worseConnLensOpts{
			// We've already established that we have too many connections at this point, so we just
			// need to match what kind we have too many of vs. what we're trying to add now.
			incomingIsBad: (numIncoming-numOutgoing > 1) && c.outgoing,
			outgoingIsBad: (numOutgoing-numIncoming > 1) && !c.outgoing,
		}, false)
		if c == nil {
			return errors.New("don't want conn")
		}
		c.close(true, false)
		t.deletePeerConn(c, false, true)
	}

	if len(t.conns) >= t.maxEstablishedConns {
		panic(len(conns))
	}

	t.conns[c] = struct{}{}

	t.cl.event.Broadcast()
	// We'll never receive the "p" extended handshake parameter.
	if !t.cl.config.DisablePEX && !c.PeerExtensionBytes.SupportsExtended() {
		c.mu.Lock()
		t.pex.Add(c)
		c.mu.Unlock()
	}

	return nil
}

func (t *Torrent) newConnsAllowed(lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	if !t.networkingEnabled.Bool() {
		return false
	}
	if t.closed.IsSet() {
		return false
	}
	if !t.needData(false) && (!t.seeding(false) || !t.haveAnyPieces(false)) {
		return false
	}
	return true
}

func (t *Torrent) wantAnyConns(lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if !t.networkingEnabled.Bool() {
		return false
	}
	if t.closed.IsSet() {
		return false
	}
	if !t.needData(false) && (!t.seeding(false) || !t.haveAnyPieces(false)) {
		return false
	}
	return len(t.conns) < t.maxEstablishedConns
}

func (t *Torrent) wantOutgoingConns(lock bool) bool {
	if !t.newConnsAllowed(lock) {
		return false
	}

	conns := t.peerConnsAsSlice(lock)
	defer conns.free()

	numOutgoingConns := conns.numOutgoingConns()
	numIncomingConns := len(conns) - numOutgoingConns

	return t.worstBadConn(worseConnLensOpts{
		incomingIsBad: numIncomingConns-numOutgoingConns > 1,
		outgoingIsBad: false,
	}, lock) != nil
}

func (t *Torrent) wantIncomingConns(lock bool) bool {
	if !t.newConnsAllowed(lock) {
		return false
	}

	conns := t.peerConnsAsSlice(lock)
	defer conns.free()

	if len(conns) < t.maxEstablishedConns {
		return true
	}

	numOutgoingConns := conns.numOutgoingConns()

	numIncomingConns := len(conns) - numOutgoingConns

	return t.worstBadConn(worseConnLensOpts{
		incomingIsBad: false,
		outgoingIsBad: numOutgoingConns-numIncomingConns > 1,
	}, lock) != nil
}

func (t *Torrent) SetMaxEstablishedConns(max int) (oldMax int) {
	t.mu.Lock()
	lenConns := len(t.conns)
	oldMax = t.maxEstablishedConns
	t.maxEstablishedConns = max
	t.mu.Unlock()

	wcs := worseConnSlice{
		conns: t.appendConns(nil, func(*PeerConn) bool {
			return true
		}, true),
	}
	wcs.initKeys(worseConnLensOpts{})
	heap.Init(&wcs)
	for lenConns > t.maxEstablishedConns && wcs.Len() > 0 {
		t.dropConnection(heap.Pop(&wcs).(*PeerConn), true, true)
		t.mu.RLock()
		lenConns = len(t.conns)
		t.mu.RUnlock()
	}
	t.openNewConns(false)
	return oldMax
}

func (t *Torrent) pieceHashed(piece pieceIndex, passed bool, hashIoErr error) {
	t.logger.LazyLog(log.Debug, func() log.Msg {
		return log.Fstr("hashed piece %d (passed=%t)", piece, passed)
	})

	p := t.piece(piece, true)
	p.mu.Lock()
	p.numVerifies++
	storageCompletionOk := p.storageCompletionOk
	p.mu.Unlock()
	t.cl.event.Broadcast()

	if t.closed.IsSet() {
		return
	}

	// Don't score the first time a piece is hashed, it could be an initial check.
	if storageCompletionOk {
		if passed {
			pieceHashedCorrect.Add(1)
		} else {
			errs := "<nil>"
			if hashIoErr != nil {
				errs = hashIoErr.Error()
			}
			p.mu.RLock()
			lenDirtiers := len(p.dirtiers)
			p.mu.RUnlock()

			log.Fmsg(
				"torrent: %s, piece %d failed hash: %d connections contributed, err: %s", t.Name(), piece, lenDirtiers, errs,
			).AddValues(t, p).LogLevel(log.Debug, t.logger)

			pieceHashedNotCorrect.Add(1)
		}
	}

	func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		p.marking = true
		t.publishPieceStateChange(piece, false)
	}()

	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		p.marking = false
		t.publishPieceStateChange(piece, false)
	}()

	if passed {
		p.mu.RLock()
		if len(p.dirtiers) != 0 {
			// Don't increment stats above connection-level for every involved connection.
			t.allStats((*ConnStats).incrementPiecesDirtiedGood)
		}
		for c := range p.dirtiers {
			c._stats.incrementPiecesDirtiedGood()
		}
		p.mu.RUnlock()
		t.clearPieceTouchers(piece, true)

		if p.hasDirtyChunks(true) {
			p.Flush() // You can be synchronous here!
		}

		err := p.Storage().MarkComplete()
		if err != nil {
			t.logger.Levelf(log.Warning, "%T: error marking piece complete %d: %s", t.storage, piece, err)
		}
		if t.closed.IsSet() {
			return
		}
		t.pendAllChunkSpecs(piece, true)
	} else {
		if p.allChunksDirty(true) && hashIoErr == nil {
			p.mu.RLock()

			if len(p.dirtiers) == 0 {
				p.mu.RUnlock()
			} else {
				// Peers contributed to all the data for this piece hash failure, and the failure was
				// not due to errors in the storage (such as data being dropped in a cache).

				// Increment Torrent and above stats, and then specific connections.
				t.allStats((*ConnStats).incrementPiecesDirtiedBad)
				bannableTouchers := make([]*Peer, 0, len(p.dirtiers))

				for c := range p.dirtiers {
					if !c.trusted {
						bannableTouchers = append(bannableTouchers, c)
					}
					// Y u do dis peer?!
					c.stats().incrementPiecesDirtiedBad()
				}

				p.mu.RUnlock()

				t.clearPieceTouchers(piece, true)
				slices.Sort(bannableTouchers, connLessTrusted)

				if t.cl.config.Debug {
					t.logger.Printf(
						"bannable conns by trust for piece %d: %v",
						piece,
						func() (ret []connectionTrust) {
							for _, c := range bannableTouchers {
								ret = append(ret, c.trust())
							}
							return
						}(),
					)
				}

				if len(bannableTouchers) >= 1 {
					c := bannableTouchers[0]
					if len(bannableTouchers) != 1 {
						t.logger.Levelf(log.Debug, "would have banned %v for touching piece %v after failed piece check", c.remoteIp(), piece)
					} else {
						// Turns out it's still useful to ban peers like this because if there's only a
						// single peer for a piece, and we never progress that piece to completion, we
						// will never smart-ban them. Discovered in
						// https://github.com/anacrolix/torrent/issues/715.
						t.logger.Levelf(log.Warning, "banning %v for being sole dirtier of piece %v after failed piece check", c, piece)
						c.ban()
					}
				}
			}
		}
		t.onIncompletePiece(piece, true)
		p.Storage().MarkNotComplete()
	}
	t.updatePieceCompletion(piece, true)
}

func (t *Torrent) cancelRequestsForPiece(piece pieceIndex, lock bool) {
	start := t.pieceRequestIndexOffset(piece, lock)
	end := start + t.pieceNumChunks(piece)
	for ri := start; ri < end; ri++ {
		t.cancelRequest(ri, true, lock, true)
	}
}

func (t *Torrent) onPieceCompleted(piece pieceIndex, lock bool) {
	t.pendAllChunkSpecs(piece, lock)
	t.cancelRequestsForPiece(piece, lock)
	t.piece(piece, lock).readerCond.Broadcast()
	conns := t.peerConnsAsSlice(lock)
	defer conns.free()
	for _, conn := range conns {
		conn.have(piece)
		t.maybeDropMutuallyCompletePeer(conn, lock, true)
	}
}

// Called when a piece is found to be not complete.
func (t *Torrent) onIncompletePiece(piece pieceIndex, lock bool) {
	if t.pieceAllDirty(piece, lock) {
		t.pendAllChunkSpecs(piece, lock)
	}
	if !t.wantPieceIndex(piece, lock) {
		// t.logger.Printf("piece %d incomplete and unwanted", piece)
		return
	}
	// We could drop any connections that we told we have a piece that we
	// don't here. But there's a test failure, and it seems clients don't care
	// if you request pieces that you already claim to have. Pruning bad
	// connections might just remove any connections that aren't treating us
	// favourably anyway.

	// for c := range t.conns {
	// 	if c.sentHave(piece) {
	// 		c.drop()
	// 	}
	// }
	for _, conn := range t.peersAsSlice(lock) {
		if conn.peerHasPiece(piece, true, lock) {
			conn.updateRequests("piece incomplete", true, lock)
		}
	}
}

func (t *Torrent) tryCreateMorePieceHashers(lock bool) {
	if !t.closed.IsSet() {
		func() {
			if lock {
				t.mu.Lock()
				defer t.mu.Unlock()
			}
			if t.hashResults == nil {
				t.hashResults = make(chan hashResult, t.cl.config.PieceHashersPerTorrent*16)
				go t.processHashResults()
			}
		}()
	}

	activePieceHashes := int(t.activePieceHashes.Load())

	for !t.closed.IsSet() &&
		activePieceHashes < t.cl.config.PieceHashersPerTorrent &&
		activePieceHashes < t.numPieces() &&
		activePieceHashes < int(t.numQueuedForHash(lock)) &&
		t.tryCreatePieceHasher() {
		activePieceHashes = int(t.activePieceHashes.Load())
	}
}

func (t *Torrent) tryCreatePieceHasher() bool {
	if t.storage == nil {
		return false
	}

	t.activePieceHashes.Add(1)

	go func() {
		defer func() {
			t.activePieceHashes.Add(-1)
		}()

		for !t.closed.IsSet() {
			pi, ok := t.getPieceToHash(true)
			if !ok {
				break
			}

			var p *Piece

			func() {
				t.mu.Lock()
				defer t.mu.Unlock()
				p = t.piece(pi, false)
				t.piecesQueuedForHash.Remove(bitmap.BitIndex(pi))
				p.hashing = true
				t.hashing.Add(1)
				t.publishPieceStateChange(pi, false)
				t.updatePiecePriority(pi, "Torrent.tryCreatePieceHasher", false)
			}()
			t.storageLock.RLock()

			sum, failedPeers, copyErr := t.hashPiece(p)
			correct := sum == *p.hash

			switch copyErr {
			case nil, io.EOF:
			default:
				log.Fmsg("piece %v (%s) hash failure copy error: %v", p, p.hash.HexString(), copyErr).Log(t.logger)
			}

			t.storageLock.RUnlock()

			t.mu.Lock()
			p.hashing = false
			t.hashing.Add(-1)
			t.mu.Unlock()

			t.hashResults <- hashResult{pi, correct, failedPeers, copyErr}
		}
	}()

	return true
}

type hashResult struct {
	index       int
	correct     bool
	failedPeers map[netip.Addr]struct{}
	copyErr     error
}

func (t *Torrent) processHashResults() {

	g, ctx := errgroup.WithContext(context.Background())
	_, cancel := context.WithCancel(ctx)
	// this is limited at the moment to avoid exess cpu usage
	// may need to be dynamically set depending on the queue size
	// there is a trade off though against memory usage (at the moment)
	// as if not enough results processors are active memory buffers will
	// grow leading to oom
	g.SetLimit(maxInt(runtime.NumCPU()-2, t.cl.config.PieceHashersPerTorrent/2))
	defer cancel()

	for !t.closed.IsSet() {
		results := []hashResult{<-t.hashResults}

		for done := false; !done; {
			select {
			case result := <-t.hashResults:
				results = append(results, result)
			default:
				done = true
			}
		}

		for _, result := range results {
			func(result hashResult) {
				g.Go(func() error {
					if result.correct {
						for peer := range result.failedPeers {
							t.cl.banPeerIP(peer.AsSlice())
							t.logger.Levelf(log.Debug, "smart banned %v for piece %v", peer, result.index)
						}

						t.dropBannedPeers()

						for ri := t.pieceRequestIndexOffset(result.index, true); ri < t.pieceRequestIndexOffset(result.index+1, true); ri++ {
							t.smartBanCache.ForgetBlock(ri)
						}
					}

					t.pieceHashed(result.index, result.correct, result.copyErr)
					t.updatePiecePriority(result.index, "Torrent.pieceHasher", true)
					return nil
				})
			}(result)
		}
	}
}

func (t *Torrent) getPieceToHash(lock bool) (ret pieceIndex, ok bool) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	t.piecesQueuedForHash.IterTyped(func(i pieceIndex) bool {
		if t.piece(i, false).hashing {
			return true
		}
		ret = i
		ok = true
		return false
	})
	return
}

func (t *Torrent) dropBannedPeers() {
	for _, p := range t.peersAsSlice(true) {
		remoteIp := p.remoteIp()
		if remoteIp == nil {
			if p.bannableAddr.Ok {
				t.logger.WithDefaultLevel(log.Debug).Printf("can't get remote ip for peer %v", p)
			}
			continue
		}
		netipAddr := netip.MustParseAddr(remoteIp.String())
		if g.Some(netipAddr) != p.bannableAddr {
			t.logger.WithDefaultLevel(log.Debug).Printf(
				"peer remote ip does not match its bannable addr [peer=%v, remote ip=%v, bannable addr=%v]",
				p, remoteIp, p.bannableAddr)
			continue
		}

		t.cl.badPeerIPsMu.RLock()
		_, ok := t.cl.badPeerIPs[netipAddr]
		t.cl.badPeerIPsMu.RUnlock()

		if ok {
			// Should this be a close?
			p.drop(true, true)
			t.logger.WithDefaultLevel(log.Debug).Printf("dropped %v for banned remote IP %v", p, netipAddr)
		}
	}
}

// Return the connections that touched a piece, and clear the entries while doing it.
func (t *Torrent) clearPieceTouchers(pi pieceIndex, lock bool) {
	p := t.piece(pi, lock)

	// get the dirtiers first so we can presever lock order (peer,piece)
	// in the code below
	dirtiers := func() []*Peer {
		p.mu.Lock()
		defer p.mu.Unlock()
		dirtiers := make([]*Peer, 0, len(p.dirtiers))
		for c := range p.dirtiers {
			dirtiers = append(dirtiers, c)
		}
		return dirtiers
	}()

	for _, c := range dirtiers {
		func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			p.mu.Lock()
			defer p.mu.Unlock()
			delete(c.peerTouchedPieces, pi)
			delete(p.dirtiers, c)
		}()
	}
}

func (t *Torrent) peersAsSlice(lock bool) (ret []*Peer) {
	t.iterPeers(func(p *Peer) {
		ret = append(ret, p)
	}, lock)
	return
}

func (t *Torrent) queuePieceCheck(pieceIndex pieceIndex, lock bool) {
	piece := t.piece(pieceIndex, lock)
	if piece.queuedForHash(lock) {
		return
	}

	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}
	t.piecesQueuedForHash.Add(bitmap.BitIndex(pieceIndex))
	t.publishPieceStateChange(pieceIndex, false)
	t.updatePiecePriority(pieceIndex, "Torrent.queuePieceCheck", false)
	t.tryCreateMorePieceHashers(false)
}

// Forces all the pieces to be re-hashed. See also Piece.VerifyData. This should not be called
// before the Info is available.
func (t *Torrent) VerifyData() {
	for i := pieceIndex(0); i < t.NumPieces(); i++ {
		t.Piece(i).VerifyData()
	}
}

func (t *Torrent) connectingToPeerAddr(addrStr string, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return len(t.halfOpen[addrStr]) != 0
}

func (t *Torrent) hasPeerConnForAddr(x PeerRemoteAddr, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	addrStr := x.String()
	for c := range t.conns {
		ra := c.RemoteAddr
		if ra.String() == addrStr {
			return true
		}
	}
	return false
}

func (t *Torrent) getHalfOpenPath(addrStr string, attemptKey outgoingConnAttemptKey, lock bool) nestedmaps.Path[*PeerInfo] {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	return nestedmaps.Next(nestedmaps.Next(nestedmaps.Begin(&t.halfOpen), addrStr), attemptKey)
}

func (t *Torrent) addHalfOpen(addrStr string, attemptKey *PeerInfo, lock bool) {
	path := t.getHalfOpenPath(addrStr, attemptKey, lock)
	if path.Exists() {
		panic("should be unique")
	}
	path.Set(attemptKey)
	t.cl.numHalfOpen++
}

// Start the process of connecting to the given peer for the given torrent if appropriate. I'm not
// sure all the PeerInfo fields are being used.
func initiateConn(opts outgoingConnOpts, ignoreLimits bool, lock bool) {
	t := opts.t
	peer := opts.peerInfo
	if peer.Id == t.cl.peerID {
		return
	}
	if t.cl.badPeerAddr(peer.Addr) && !peer.Trusted {
		return
	}
	addr := peer.Addr
	addrStr := addr.String()
	if !ignoreLimits {
		if t.connectingToPeerAddr(addrStr, lock) {
			return
		}
	}
	if t.hasPeerConnForAddr(addr, lock) {
		return
	}
	attemptKey := &peer
	t.addHalfOpen(addrStr, attemptKey, lock)
	go t.cl.outgoingConnection(
		opts,
		attemptKey,
	)
}

// Adds a trusted, pending peer for each of the given Client's addresses. Typically used in tests to
// quickly make one Client visible to the Torrent of another Client.
func (t *Torrent) AddClientPeer(cl *Client) int {
	return t.AddPeers(func() (ps []PeerInfo) {
		for _, la := range cl.ListenAddrs() {
			ps = append(ps, PeerInfo{
				Addr:    la,
				Trusted: true,
			})
		}
		return
	}())
}

// All stats that include this Torrent. Useful when we want to increment ConnStats but not for every
// connection.
func (t *Torrent) allStats(f func(*ConnStats)) {
	f(&t.connStats)
	f(&t.cl.connStats)
}

func (t *Torrent) hashingPiece(i pieceIndex, lock bool) bool {
	return t.piece(i, lock).hashing
}

func (t *Torrent) pieceQueuedForHash(i pieceIndex, lock bool) bool {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return t.piecesQueuedForHash.Get(bitmap.BitIndex(i))
}

func (t *Torrent) numQueuedForHash(lock bool) uint64 {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return t.piecesQueuedForHash.Len()
}

func (t *Torrent) dialTimeout(lock bool) time.Duration {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	return reducedDialTimeout(t.cl.config.MinDialTimeout, t.cl.config.NominalDialTimeout, t.cl.config.HalfOpenConnsPerTorrent, t.peers.Len())
}

func (t *Torrent) piece(i int, lock bool) *Piece {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	return &t.pieces[i]
}

func (t *Torrent) onWriteChunkErr(err error) {
	if t.userOnWriteChunkErr != nil {
		go t.userOnWriteChunkErr(err)
		return
	}
	t.logger.WithDefaultLevel(log.Critical).Printf("default chunk write error handler: disabling data download")
	t.DisallowDataDownload()
}

func (t *Torrent) DisallowDataDownload() {
	conns := t.peerConnsAsSlice(true)
	defer conns.free()
	t.dataDownloadDisallowed.Set()

	for _, c := range conns {
		func() {
			// Could check if peer request state is empty/not interested?
			c.updateRequests("disallow data download", true, true)
			c.cancelAllRequests(true)
		}()
	}
}

func (t *Torrent) AllowDataDownload() {
	//t.mu.Lock()
	//defer t.mu.Unlock()

	conns := t.peerConnsAsSlice(true)
	defer conns.free()
	t.dataDownloadDisallowed.Clear()

	for _, c := range conns {
		c.updateRequests("allow data download", true, true)
	}
}

// Enables uploading data, if it was disabled.
func (t *Torrent) AllowDataUpload() {
	t.mu.Lock()
	conns := t.peerConnsAsSlice(false)
	defer conns.free()
	t.dataUploadDisallowed = false
	t.mu.Unlock()

	for _, c := range conns {
		c.updateRequests("allow data upload", true, true)
	}
}

// Disables uploading data, if it was enabled.
func (t *Torrent) DisallowDataUpload() {
	t.mu.Lock()
	conns := t.peerConnsAsSlice(false)
	defer conns.free()
	t.dataUploadDisallowed = true
	t.mu.Unlock()

	for _, c := range conns {
		// TODO: This doesn't look right. Shouldn't we tickle writers to choke peers or something instead?
		c.updateRequests("disallow data upload", true, true)
	}
}

// Sets a handler that is called if there's an error writing a chunk to local storage. By default,
// or if nil, a critical message is logged, and data download is disabled.
func (t *Torrent) SetOnWriteChunkError(f func(error)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.userOnWriteChunkErr = f
}

func (t *Torrent) iterPeers(f func(p *Peer), lock bool) {
	conns := t.peerConnsAsSlice(lock)
	defer conns.free()

	webSeeds := t.webSeedsAsSlice(lock)

	for _, pc := range conns {
		f(&pc.Peer)
	}

	for _, ws := range webSeeds {
		f(ws)
	}
}

func (t *Torrent) callbacks() *Callbacks {
	return &t.cl.config.Callbacks
}

type AddWebSeedsOpt func(*webseed.Client)

// Sets the WebSeed trailing path escaper for a webseed.Client.
func WebSeedPathEscaper(custom webseed.PathEscaper) AddWebSeedsOpt {
	return func(c *webseed.Client) {
		c.PathEscaper = custom
	}
}

func (t *Torrent) AddWebSeeds(urls []string, opts ...AddWebSeedsOpt) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, u := range urls {
		t.addWebSeed(u, false, opts...)
	}
}

func (t *Torrent) addWebSeed(url string, lock bool, opts ...AddWebSeedsOpt) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if t.cl.config.DisableWebseeds {
		return
	}
	if _, ok := t.webSeeds[url]; ok {
		return
	}
	// I don't think Go http supports pipelining requests. However, we can have more ready to go
	// right away. This value should be some multiple of the number of connections to a host. I
	// would expect that double maxRequests plus a bit would be appropriate. This value is based on
	// downloading Sintel (08ada5a7a6183aae1e09d831df6748d566095a10) from
	// "https://webtorrent.io/torrents/".
	const maxRequests = 16

	// This should affect how often we have to recompute requests for this peer. Note that
	// because we can request more than 1 thing at a time over HTTP, we will hit the low
	// requests mark more often, so recomputation is probably sooner than with regular peer
	// conns. ~4x maxRequests would be about right.
	peerMaxRequests := 128

	// unless there is more availible bandwith - in which case max requests becomes
	// the max available pieces that will consune that bandwidth

	if bandwidth := t.cl.config.DownloadRateLimiter.Limit(); bandwidth > 0 && t.info != nil {
		if maxPieceRequests := int(bandwidth / rate.Limit(t.info.PieceLength)); maxPieceRequests > peerMaxRequests {
			peerMaxRequests = maxPieceRequests
		}
	}

	ws := webseedPeer{
		peer: Peer{
			t:                        t,
			outgoing:                 true,
			Network:                  "http",
			reconciledHandshakeStats: true,
			PeerMaxRequests:          peerMaxRequests,
			// TODO: Set ban prefix?
			RemoteAddr: remoteAddrFromUrl(url),
			callbacks:  t.callbacks(),
		},
		client: webseed.Client{
			HttpClient: t.cl.httpClient,
			Url:        url,
		},
		maxRequesters:  maxRequests,
		activeRequests: make(map[Request]webseed.Request, maxRequests),
		// Limit requests rather than responses - becuase otherwise
		// the go http layer buffers causing memory growth
		requestRateLimiter: t.cl.config.DownloadRateLimiter,
	}

	ws.peer.initRequestState()
	for _, opt := range opts {
		opt(&ws.client)
	}
	ws.peer.initUpdateRequestsTimer(true)
	ws.requesterCond.L = &sync.Mutex{}
	for i := 0; i < ws.maxRequesters; i += 1 {
		go ws.requester(i)
	}
	for _, f := range t.callbacks().NewPeer {
		f(&ws.peer)
	}
	ws.peer.logger = t.logger.WithContextValue(&ws)
	ws.peer.peerImpl = &ws
	t.webSeeds[url] = &ws.peer
	if t.haveInfo(false) {
		ws.onGotInfo(t.info, true, false)
	}
}

func (t *Torrent) peerIsActive(p *Peer) (active bool) {
	t.iterPeers(func(p1 *Peer) {
		if p1 == p {
			active = true
		}
	}, true)
	return
}

func (t *Torrent) requestIndexToRequest(ri RequestIndex, lock bool) Request {
	index := t.pieceIndexOfRequestIndex(ri, lock)
	return Request{
		pp.Integer(index),
		t.piece(index, lock).chunkIndexSpec(ri % t.chunksPerRegularPiece(lock)),
	}
}

func (t *Torrent) requestIndexFromRequest(r Request, lock bool) RequestIndex {
	return t.pieceRequestIndexOffset(pieceIndex(r.Index), lock) + RequestIndex(r.Begin/t.chunkSize)
}

func (t *Torrent) pieceRequestIndexOffset(piece pieceIndex, lock bool) RequestIndex {
	return RequestIndex(piece) * t.chunksPerRegularPiece(lock)
}

func (t *Torrent) updateComplete(lock bool) {
	t.Complete.SetBool(t.haveAllPieces(lock))
}

func (t *Torrent) cancelRequest(r RequestIndex, updateRequests, lock bool, lockPeer bool) *Peer {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	p := t.requestingPeer(r, false)
	if p != nil {
		p.cancel(r, updateRequests, lockPeer, false)
	}
	// TODO: This is a check that an old invariant holds. It can be removed after some testing.
	//delete(t.pendingRequests, r)
	if _, ok := t.requestState[r]; ok {
		panic("expected request state to be gone after cancel")
	}
	return p
}

func (t *Torrent) requestingPeer(r RequestIndex, lock bool) *Peer {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}
	if state, ok := t.requestState[r]; ok {
		return state.peer
	}
	return nil
}

func (t *Torrent) addConnWithAllPieces(p *Peer, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if t.connsWithAllPieces == nil {
		t.connsWithAllPieces = make(map[*Peer]struct{}, t.maxEstablishedConns)
	}
	t.connsWithAllPieces[p] = struct{}{}
}

func (t *Torrent) deleteConnWithAllPieces(p *Peer, lock bool) bool {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	_, ok := t.connsWithAllPieces[p]
	delete(t.connsWithAllPieces, p)
	return ok
}

func (t *Torrent) numActivePeers() int {
	return len(t.conns) + len(t.webSeeds)
}

func (t *Torrent) hasStorageCap() bool {
	f := t.storage.Capacity
	if f == nil {
		return false
	}
	_, ok := (*f)()
	return ok
}

func (t *Torrent) pieceIndexOfRequestIndex(ri RequestIndex, lock bool) pieceIndex {
	return pieceIndex(ri / t.chunksPerRegularPiece(lock))
}

func (t *Torrent) iterUndirtiedRequestIndexesInPiece(
	iter *typedRoaring.Iterator[RequestIndex],
	piece pieceIndex,
	f func(RequestIndex),
	lock bool,
) {
	pieceRequestIndexOffset := t.pieceRequestIndexOffset(piece, lock)
	iterBitmapUnsetInRange(
		iter,
		pieceRequestIndexOffset, pieceRequestIndexOffset+t.pieceNumChunks(piece),
		f,
	)
}

type requestState struct {
	peer *Peer
	when time.Time
}

// Returns an error if a received chunk is out of bounds in someway.
func (t *Torrent) checkValidReceiveChunk(r Request) error {
	if !t.haveInfo(true) {
		return errors.New("torrent missing info")
	}
	if int(r.Index) >= t.numPieces() {
		return fmt.Errorf("chunk index %v, torrent num pieces %v", r.Index, t.numPieces())
	}
	pieceLength := t.pieceLength(pieceIndex(r.Index))
	if r.Begin >= pieceLength {
		return fmt.Errorf("chunk begins beyond end of piece (%v >= %v)", r.Begin, pieceLength)
	}
	// We could check chunk lengths here, but chunk request size is not changed often, and tricky
	// for peers to manipulate as they need to send potentially large buffers to begin with. There
	// should be considerable checks elsewhere for this case due to the network overhead. We should
	// catch most of the overflow manipulation stuff by checking index and begin above.
	return nil
}

func (t *Torrent) peerConnsWithDialAddrPort(target netip.AddrPort) (ret []*PeerConn) {
	for pc := range t.conns {
		dialAddr, err := pc.remoteDialAddrPort()
		if err != nil {
			continue
		}
		if dialAddr != target {
			continue
		}
		ret = append(ret, pc)
	}
	return
}

func wrapUtHolepunchMsgForPeerConn(
	recipient *PeerConn,
	msg utHolepunch.Msg,
) pp.Message {
	extendedPayload, err := msg.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return pp.Message{
		Type:            pp.Extended,
		ExtendedID:      g.MapMustGet(recipient.PeerExtensionIDs, utHolepunch.ExtensionName),
		ExtendedPayload: extendedPayload,
	}
}

func sendUtHolepunchMsg(
	pc *PeerConn,
	msgType utHolepunch.MsgType,
	addrPort netip.AddrPort,
	errCode utHolepunch.ErrCode,
) {
	holepunchMsg := utHolepunch.Msg{
		MsgType:  msgType,
		AddrPort: addrPort,
		ErrCode:  errCode,
	}
	incHolepunchMessagesSent(holepunchMsg)
	ppMsg := wrapUtHolepunchMsgForPeerConn(pc, holepunchMsg)
	pc.write(ppMsg, true)
}

func incHolepunchMessages(msg utHolepunch.Msg, verb string) {
	torrent.Add(
		fmt.Sprintf(
			"holepunch %v %v messages %v",
			msg.MsgType,
			addrPortProtocolStr(msg.AddrPort),
			verb,
		),
		1,
	)
}

func incHolepunchMessagesReceived(msg utHolepunch.Msg) {
	incHolepunchMessages(msg, "received")
}

func incHolepunchMessagesSent(msg utHolepunch.Msg) {
	incHolepunchMessages(msg, "sent")
}

func (t *Torrent) handleReceivedUtHolepunchMsg(msg utHolepunch.Msg, sender *PeerConn) error {
	incHolepunchMessagesReceived(msg)
	switch msg.MsgType {
	case utHolepunch.Rendezvous:
		t.logger.Printf("got holepunch rendezvous request for %v from %p", msg.AddrPort, sender)
		sendMsg := sendUtHolepunchMsg
		senderAddrPort, err := sender.remoteDialAddrPort()
		if err != nil {
			sender.logger.Levelf(
				log.Warning,
				"error getting ut_holepunch rendezvous sender's dial address: %v",
				err,
			)
			// There's no better error code. The sender's address itself is invalid. I don't see
			// this error message being appropriate anywhere else anyway.
			sendMsg(sender, utHolepunch.Error, msg.AddrPort, utHolepunch.NoSuchPeer)
		}
		targets := t.peerConnsWithDialAddrPort(msg.AddrPort)
		if len(targets) == 0 {
			sendMsg(sender, utHolepunch.Error, msg.AddrPort, utHolepunch.NotConnected)
			return nil
		}
		for _, pc := range targets {
			if !pc.supportsExtension(utHolepunch.ExtensionName, true) {
				sendMsg(sender, utHolepunch.Error, msg.AddrPort, utHolepunch.NoSupport)
				continue
			}
			sendMsg(sender, utHolepunch.Connect, msg.AddrPort, 0)
			sendMsg(pc, utHolepunch.Connect, senderAddrPort, 0)
		}
		return nil
	case utHolepunch.Connect:
		holepunchAddr := msg.AddrPort
		t.logger.Printf("got holepunch connect request for %v from %p", holepunchAddr, sender)
		if g.MapContains(t.cl.undialableWithoutHolepunch, holepunchAddr) {
			setAdd(&t.cl.undialableWithoutHolepunchDialedAfterHolepunchConnect, holepunchAddr)
			if g.MapContains(t.cl.accepted, holepunchAddr) {
				setAdd(&t.cl.probablyOnlyConnectedDueToHolepunch, holepunchAddr)
			}
		}
		opts := outgoingConnOpts{
			peerInfo: PeerInfo{
				Addr:         msg.AddrPort,
				Source:       PeerSourceUtHolepunch,
				PexPeerFlags: sender.pex.remoteLiveConns[msg.AddrPort].UnwrapOrZeroValue(),
			},
			t: t,
			// Don't attempt to start our own rendezvous if we fail to connect.
			skipHolepunchRendezvous:  true,
			receivedHolepunchConnect: true,
			// Assume that the other end initiated the rendezvous, and will use our preferred
			// encryption. So we will act normally.
			HeaderObfuscationPolicy: t.cl.config.HeaderObfuscationPolicy,
		}
		initiateConn(opts, true, true)
		return nil
	case utHolepunch.Error:
		torrent.Add("holepunch error messages received", 1)
		t.logger.Levelf(log.Debug, "received ut_holepunch error message from %v: %v", sender, msg.ErrCode)
		return nil
	default:
		return fmt.Errorf("unhandled msg type %v", msg.MsgType)
	}
}

func addrPortProtocolStr(addrPort netip.AddrPort) string {
	addr := addrPort.Addr()
	switch {
	case addr.Is4():
		return "ipv4"
	case addr.Is6():
		return "ipv6"
	default:
		panic(addrPort)
	}
}

func (t *Torrent) trySendHolepunchRendezvous(addrPort netip.AddrPort) error {
	rzsSent := 0
	conns := t.peerConnsAsSlice(true)
	defer conns.free()

	for _, pc := range conns {
		if !pc.supportsExtension(utHolepunch.ExtensionName, true) {
			continue
		}
		if pc.supportsExtension(pp.ExtensionNamePex, true) {
			if !g.MapContains(pc.pex.remoteLiveConns, addrPort) {
				continue
			}
		}
		t.logger.Levelf(log.Debug, "sent ut_holepunch rendezvous message to %v for %v", pc, addrPort)
		sendUtHolepunchMsg(pc, utHolepunch.Rendezvous, addrPort, 0)
		rzsSent++
	}
	if rzsSent == 0 {
		return errors.New("no eligible relays")
	}
	return nil
}

func (t *Torrent) numHalfOpenAttempts(lock bool) (num int) {
	if lock {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	for _, attempts := range t.halfOpen {
		num += len(attempts)
	}
	return
}
