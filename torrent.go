package torrent

import (
	"container/heap"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/pkg/errors"

	"github.com/anacrolix/missinggo/pubsub"
	"github.com/anacrolix/missinggo/slices"
	"github.com/anacrolix/missinggo/v2"
	"github.com/james-lawrence/torrent/dht"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/langx"

	"github.com/james-lawrence/torrent/bencode"
	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/storage"
	"github.com/james-lawrence/torrent/tracker"
)

// Tuner runtime tuning of an actively running torrent.
type Tuner func(*torrent)

// TuneMaxConnections adjust the maximum connections allowed for a torrent.
func TuneMaxConnections(m int) Tuner {
	return func(t *torrent) {
		t.SetMaxEstablishedConns(m)
	}
}

// TunePeers add peers to the torrent.
func TunePeers(peers ...Peer) Tuner {
	return func(t *torrent) {
		t.AddPeers(peers)
	}
}

// TuneClientPeer adds a trusted, pending peer for each of the Client's addresses.
// used for tests.
func TuneClientPeer(cl *Client) Tuner {
	return func(t *torrent) {
		ps := []Peer{}
		for _, la := range cl.ListenAddrs() {
			ps = append(ps, Peer{
				IP:      missinggo.AddrIP(la),
				Port:    missinggo.AddrPort(la),
				Trusted: true,
			})
		}
		t.AddPeers(ps)
	}
}

// add trackers to the torrent.
func TuneTrackers(trackers ...[]string) Tuner {
	return func(t *torrent) {
		t.addTrackers(trackers)
	}
}

// Torrent represents the state of a torrent within a client.
// interface is currently being used to ease the transition of to a cleaner API.
// Many methods should not be called before the info is available,
// see .Info and .GotInfo.
type Torrent interface {
	Metadata() Metadata
	Tune(...Tuner) error
	Name() string                 // TODO: remove, should be pulled from Metadata()
	Metainfo() metainfo.MetaInfo  // TODO: remove, should be pulled from Metadata()
	BytesCompleted() int64        // TODO: maybe should be pulled from torrent, it has a reference to the storage implementation. or maybe part of the Stats call?
	Stats() TorrentStats          // TODO: rename TorrentStats, it stutters.
	Info() *metainfo.Info         // TODO: remove, this should be pulled from Metadata()
	GotInfo() <-chan struct{}     // TODO: remove, torrents should never be returned if they don't have the meta info.
	Storage() storage.TorrentImpl // temporary replacement for reader.
}

// Download a torrent into a writer blocking until completion.
func DownloadInto(ctx context.Context, dst io.Writer, m Torrent, options ...Tuner) (n int64, err error) {
	if err = m.Tune(options...); err != nil {
		return 0, err
	}

	select {
	case <-m.GotInfo():
	case <-ctx.Done():
		return 0, errorsx.Compact(context.Cause(ctx), ctx.Err())
	}

	if n, err = io.Copy(dst, NewReader(m)); err != nil {
		return n, err
	} else if n != m.Info().TotalLength() {
		return n, errors.Errorf("download failed, missing data %d != %d", n, m.Info().TotalLength())
	}

	return n, nil
}

func Verify(ctx context.Context, t Torrent) error {
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return errorsx.Compact(context.Cause(ctx), ctx.Err())
	}

	tmp := t.(*torrent)

	for i := 0; i < tmp.numPieces(); i++ {
		tmp.digests.Enqueue(i)
	}

	return nil
}

func newTorrent(cl *Client, src Metadata) *torrent {
	m := &sync.RWMutex{}
	chunkcond := sync.NewCond(&sync.Mutex{})
	t := &torrent{
		displayName: src.DisplayName,
		infoHash:    src.InfoHash,
		cln:         cl,
		config:      cl.config,
		_mu:         m,
		peers: newPeerPool(32, func(p Peer) peerPriority {
			return bep40PriorityIgnoreError(cl.publicAddr(p.IP), p.addr())
		}),
		conns:                   newconnset(2 * cl.config.EstablishedConnsPerTorrent),
		halfOpen:                make(map[string]Peer),
		_halfOpenmu:             &sync.RWMutex{},
		pieceStateChanges:       pubsub.NewPubSub(),
		storageOpener:           storage.NewClient(src.Storage),
		maxEstablishedConns:     cl.config.EstablishedConnsPerTorrent,
		networkingEnabled:       true,
		duplicateRequestTimeout: time.Second,
		chunkcond:               chunkcond,
		chunks:                  newChunks(langx.DefaultIfZero(16*bytesx.KiB, src.ChunkSize), metainfo.NewInfo(), chunkoptCond(chunkcond)),
		pex:                     newPex(),
		storage:                 storage.NewZero(),
		digests:                 new(digests),
	}
	t.metadataChanged = sync.Cond{L: tlocker{torrent: t}}
	t.event = &sync.Cond{L: tlocker{torrent: t}}
	t.setChunkSize(pp.Integer(langx.DefaultIfZero(16*bytesx.KiB, src.ChunkSize)))
	*t.digests = newDigestsFromTorrent(t)
	return t
}

func newconnset(n int) *conns {
	return &conns{
		m: make(map[*connection]struct{}, n),
	}
}

type conns struct {
	_m sync.RWMutex
	m  map[*connection]struct{}
}

func (t *conns) insert(c *connection) {
	t._m.Lock()
	defer t._m.Unlock()
	t.m[c] = struct{}{}
}

func (t *conns) delete(c *connection) (int, bool) {
	t._m.Lock()
	defer t._m.Unlock()
	olen := len(t.m)
	delete(t.m, c)
	nlen := len(t.m)
	return nlen, olen != nlen
}

func (t *conns) filtered(ignore func(*connection) bool) (ret []*connection) {
	t._m.RLock()
	defer t._m.RUnlock()
	ret = make([]*connection, 0, len(t.m))
	for conn := range t.m {
		if ignore(conn) {
			continue
		}
		ret = append(ret, conn)
	}
	return ret
}

func (t *conns) list() (ret []*connection) {
	t._m.RLock()
	defer t._m.RUnlock()
	ret = make([]*connection, 0, len(t.m))
	for conn := range t.m {
		ret = append(ret, conn)
	}
	return ret
}

func (t *conns) length() int {
	t._m.RLock()
	defer t._m.RUnlock()
	return len(t.m)
}

// Maintains state of torrent within a Client.
type torrent struct {
	// Torrent-level aggregate statistics. First in struct to ensure 64-bit
	// alignment. See #262.
	stats ConnStats

	numReceivedConns int64

	cln    *Client
	config *ClientConfig
	_mu    *sync.RWMutex

	networkingEnabled bool

	// How long to avoid duplicating a pending request.
	duplicateRequestTimeout time.Duration

	closed   missinggo.Event
	infoHash metainfo.Hash
	// Values are the piece indices that changed.
	pieceStateChanges *pubsub.PubSub
	// The size of chunks to request from peers over the wire. This is
	// normally 16KiB by convention these days.
	chunkSize pp.Integer
	chunkcond *sync.Cond
	chunkPool *sync.Pool

	// The storage to open when the info dict becomes available.
	storageOpener *storage.Client
	// Storage for torrent data.
	storage storage.TorrentImpl
	// Read-locked for using storage, and write-locked for Closing.
	storageLock sync.RWMutex

	// TODO: Only announce stuff is used?
	metainfo metainfo.MetaInfo

	// The info dict. nil if we don't have it (yet).
	info  *metainfo.Info
	files []*File

	// Active peer connections, running message stream loops. TODO: Make this
	// open (not-closed) connections only.
	conns *conns

	maxEstablishedConns int

	// Set of addrs to which we're attempting to connect. Connections are
	// half-open until all handshakes are completed.
	halfOpen    map[string]Peer
	_halfOpenmu *sync.RWMutex

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known in
	// the swarm.
	peers          peerPool
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

	// chunks management tracks the current status of the different chunks
	chunks *chunks

	// digest management determines if pieces are valid.
	digests *digests

	// peer exchange for the current torrent
	pex *pex

	// A pool of piece priorities []int for assignment to new connections.
	// These "inclinations" are used to give connections preference for
	// different pieces.
	connPieceInclinationPool sync.Pool

	// signal events on this torrent.
	event *sync.Cond
}

// Returns a Reader bound to the torrent's data. All read calls block until
// the data requested is actually available.
func (t *torrent) Storage() storage.TorrentImpl {
	return newBlockingReader(t.storage, t.chunks, t.digests)
}

// Metadata provides enough information to lookup the torrent again.
func (t *torrent) Metadata() Metadata {
	return Metadata{
		DisplayName: t.name(),
		InfoHash:    t.infoHash,
		ChunkSize:   int(t.chunkSize),
		InfoBytes:   t.metadataBytes,
		Trackers:    t.metainfo.AnnounceList,
	}
}

// Tune the settings of the torrent.
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
	// log.Output(depth, fmt.Sprintf("t(%p) lock initiated - %d", t, updated))
	t._mu.Lock()
	// log.Output(depth, fmt.Sprintf("t(%p) lock completed - %d", t, updated))
}

func (t *torrent) _unlock(depth int) {
	// updated := atomic.AddUint64(&t.ucount, 1)
	// log.Output(depth, fmt.Sprintf("t(%p) unlock initiated - %d", t, updated))
	t._mu.Unlock()
	// log.Output(depth, fmt.Sprintf("t(%p) unlock completed - %d", t, updated))
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

	t._halfOpenmu.RLock()
	// Add half-open peers to the list
	for _, peer := range t.halfOpen {
		ks = append(ks, peer)
	}
	t._halfOpenmu.RUnlock()

	// Add active peers to the list
	for _, conn := range t.conns.list() {
		ks = append(ks, Peer{
			ID:     conn.PeerID,
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
	*t.chunks = *newChunks(int(size), langx.DefaultIfZero(metainfo.NewInfo(), t.info), chunkoptCond(t.chunkcond))
	t.chunkPool = &sync.Pool{
		New: func() interface{} {
			b := make([]byte, size)
			return &b
		},
	}
}

func (t *torrent) pieceComplete(piece pieceIndex) bool {
	if t.chunks == nil {
		return false
	}

	if t.chunks.completed.IsEmpty() {
		return false
	}

	return t.chunks.ChunksComplete(piece)
}

// func (t *torrent) pieceCompleteUncached(piece pieceIndex) storage.Completion {
// 	return t.pieces[piece].Storage().Completion()
// }

// There's a connection to that address already.
func (t *torrent) addrActive(addr string) bool {
	t._halfOpenmu.RLock()
	_, ok := t.halfOpen[addr]
	t._halfOpenmu.RUnlock()
	if ok {
		return true
	}

	for _, c := range t.conns.list() {
		ra := c.remoteAddr
		if ra.String() == addr {
			return true
		}
	}

	return false
}

func (t *torrent) unclosedConnsAsSlice() (ret []*connection) {
	return t.conns.filtered(func(c *connection) bool { return c.closed.IsSet() })
}

func (t *torrent) AddPeer(p Peer) {
	t.lock()
	defer t.unlock()
	t.addPeer(p)
}

func (t *torrent) addPeer(p Peer) {
	peersAddedBySource.Add(string(p.Source), 1)

	if t.closed.IsSet() {
		log.Println("torrent.addPeer closed")
		return
	}

	if t.peers.Add(p) {
		metrics.Add("peers replaced", 1)
	}

	t.openNewConns()

	for t.peers.Len() > t.config.TorrentPeersHighWater {
		if _, ok := t.peers.DeleteMin(); ok {
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
		t.config.warn().Printf("%s: ignoring metadata piece %d\n", t, index)
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

func (t *torrent) setInfo(info *metainfo.Info) (err error) {
	if err := validateInfo(info); err != nil {
		return fmt.Errorf("bad info: %s", err)
	}

	if t.storageOpener != nil {
		t.storage, err = t.storageOpener.OpenTorrent(info, t.infoHash)
		if err != nil {
			return fmt.Errorf("error opening torrent storage: %s", err)
		}
	}

	t.nameMu.Lock()
	t.info = info
	t.setChunkSize(t.chunkSize)
	t.nameMu.Unlock()

	t.initFiles()

	return nil
}

func (t *torrent) onSetInfo() {
	// log.Println("set info initiated")
	for _, conn := range t.conns.list() {
		if err := conn.setNumPieces(t.numPieces()); err != nil {
			t.config.info().Println(errors.Wrap(err, "closing connection"))
			conn.Close()
		}
	}

	t.chunks.missing.AddRange(0, uint64(t.chunks.cmaximum))
	for i := 0; i < t.numPieces(); i++ {
		t.digests.Enqueue(i)
	}

	t.chunks.FailuresReset()

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
	*t.digests = newDigestsFromTorrent(t)

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

	if bytes <= 0 || bytes > 10*bytesx.MiB { // 10MB, pulled from my ass.
		return errors.New("bad size")
	}

	if t.metadataBytes != nil && len(t.metadataBytes) == int(bytes) {
		return err
	}

	t.metadataBytes = make([]byte, bytes)
	t.metadataCompletedChunks = make([]bool, (bytes+(1<<14)-1)/(1<<14))
	t.metadataChanged.Broadcast()

	for _, c := range t.conns.list() {
		c.requestPendingMetadata()
	}

	return err
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *torrent) name() string {
	t.nameMu.RLock()
	defer t.nameMu.RUnlock()
	return t.displayName
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

	conns := t.conns.list()
	slices.Sort(conns, worseConn)
	for i, c := range conns {
		fmt.Fprintf(w, "%2d. ", i+1)
		c.WriteStatus(w, t)
	}
}

func (t *torrent) haveInfo() bool {
	return t.info != nil
}

func (t *torrent) BytesMissing() int64 {
	t.rLock()
	defer t.rUnlock()
	return t.bytesLeft()
}

func (t *torrent) bytesLeft() (left int64) {
	s := t.chunks.Snapshot(&TorrentStats{})
	return t.info.TotalLength() - ((int64(s.Unverified) * int64(t.chunks.clength)) + (int64(s.Completed) * int64(t.info.PieceLength)))
}

// Bytes left to give in tracker announces.
func (t *torrent) bytesLeftAnnounce() int64 {
	if t.haveInfo() {
		return t.bytesLeft()
	}

	return -1
}

func (t *torrent) usualPieceSize() int {
	return int(t.info.PieceLength)
}

func (t *torrent) numPieces() pieceIndex {
	return pieceIndex(t.info.NumPieces())
}

func (t *torrent) close() (err error) {
	t.lock()
	defer t.unlock()

	t.closed.Set()

	func() {
		if t.storage == nil {
			return
		}

		t.storageLock.Lock()
		defer t.storageLock.Unlock()
		t.storage.Close()
	}()

	for _, conn := range t.conns.list() {
		conn.Close()
	}

	t.event.Broadcast()

	return err
}

func (t *torrent) writeChunk(piece int, begin int64, data []byte) (err error) {
	t.lock()
	defer t.unlock()

	if len(data) > int(t.info.PieceLength) {
		return fmt.Errorf("long write")
	}

	// calculate offset for chunk
	offset := (t.info.PieceLength * int64(piece)) + begin

	n, err := t.storage.WriteAt(data, offset)
	// n, err := t.pieces[piece].Storage().WriteAt(data, begin)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}

	return err
}

func (t *torrent) pieceLength(piece pieceIndex) pp.Integer {
	if t.info.PieceLength == 0 {
		// There will be no variance amongst pieces. Only pain.
		return 0
	}

	if piece == t.numPieces()-1 {
		ret := pp.Integer(t.info.TotalLength() % t.info.PieceLength)
		if ret == 0 {
			panic("invalid zero length for final piece")
		}

		return ret
	}
	return pp.Integer(t.info.PieceLength)
}

func (t *torrent) haveAnyPieces() bool {
	return t.chunks.completed.GetCardinality() != 0
}

func (t *torrent) haveAllPieces() bool {
	if !t.haveInfo() {
		return false
	}

	return int(t.chunks.completed.GetCardinality()) == t.numPieces()
}

func (t *torrent) havePiece(index pieceIndex) bool {
	return t.haveInfo() && t.pieceComplete(index)
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

func (t *torrent) maybeNewConns() {
	// Tickle the accept routine.
	t.cln.event.Broadcast()
	t.openNewConns()
}

func (t *torrent) incrementReceivedConns(c *connection, delta int64) {
	if c.Discovery == peerSourceIncoming {
		atomic.AddInt64(&t.numReceivedConns, delta)
	}
}

func (t *torrent) dropHalfOpen(addr string) {
	t._halfOpenmu.RLock()
	_, ok := t.halfOpen[addr]
	t._halfOpenmu.RUnlock()
	if !ok {
		panic("invariant broken")
	}

	t._halfOpenmu.Lock()
	delete(t.halfOpen, addr)
	t._halfOpenmu.Unlock()
}

func (t *torrent) openNewConns() {
	var (
		ok bool
		p  Peer
	)

	defer t.updateWantPeersEvent()
	for {
		if !t.wantConns() {
			return
		}

		if p, ok = t.peers.PopMax(); !ok {
			return
		}

		t.initiateConn(context.Background(), p)
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

// Non-blocking read. Client lock is not required.
func (t *torrent) readAt(b []byte, off int64) (n int, err error) {
	return t.storage.ReadAt(b, off)
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

	t.config.debug().Printf("%s: got metadata from peers", t)

	return nil
}

func (t *torrent) needData() bool {
	if t.closed.IsSet() {
		return false
	}

	if !t.haveInfo() {
		return true
	}

	return t.chunks.Missing() != 0
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

func (t *torrent) dropConnection(c *connection) {
	if t.deleteConnection(c) {
		t.openNewConns()
	}

	t.event.Broadcast()
}

// Returns true if connection is removed from torrent.Conns.
func (t *torrent) deleteConnection(c *connection) (ret bool) {
	t.pex.dropped(c)
	c.Close()
	// l2.Printf("closed c(%p) - pending(%d)\n", c, len(c.requests))
	nlen, ret := t.conns.delete(c)

	if nlen == 0 {
		t.assertNoPendingRequests()
	}

	return ret
}

func (t *torrent) assertNoPendingRequests() {
	if outstanding := t.chunks.Outstanding(); len(outstanding) != 0 {
		for _, r := range outstanding {
			t.config.errors().Printf("still expecting c(%p) d(%020d) r(%d,%d,%d)", t.chunks, r.Digest, r.Index, r.Begin, r.Length)
		}
		// panic(t.chunks.outstanding)
	}
}

func (t *torrent) wantPeers() bool {
	if t.closed.IsSet() {
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
			t.config.info().Println("error parsing tracker url:", _url)
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
	// l := rate.NewLimiter(rate.Every(time.Minute), 1)
	for v := range pvs {
		// if len(v.Peers) > 0 || l.Allow() {
		// 	log.Println("received peers", t.infoHash, len(v.Peers))
		// }
		for _, cp := range v.Peers {
			if cp.Port() == 0 {
				// Can't do anything with this.
				continue
			}

			t.AddPeer(Peer{
				IP:     cp.Addr().AsSlice(),
				Port:   int(cp.Port()),
				Source: peerSourceDhtGetPeers,
			})
		}
	}
}

func (t *torrent) announceToDht(impliedPort bool, s *dht.Server) error {
	ctx, done := context.WithTimeout(context.Background(), 5*time.Minute)
	defer done()

	ps, err := s.Announce(ctx, t.infoHash, t.cln.incomingPeerPort(), impliedPort)
	if err != nil {
		return err
	}

	defer ps.Close()
	go t.consumeDhtAnnouncePeers(ps.Peers)

	select {
	case <-t.closed.LockedChan(t.locker()):
	case <-ctx.Done():
	}

	return nil
}

func (t *torrent) dhtAnnouncer(s *dht.Server) {
	for {
		select {
		case <-t.closed.LockedChan(t.locker()):
			return
		case <-t.wantPeersEvent.LockedChan(t.locker()):
		}

		t.stats.DHTAnnounce.Add(1)

		if err := t.announceToDht(true, s); err != nil {
			t.config.info().Println(t, errors.Wrap(err, "error announcing to DHT"))
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
	ret.Seeding = t.seeding()
	ret.ActivePeers = len(t.conns.list())
	ret.HalfOpenPeers = len(t.halfOpen)
	ret.PendingPeers = t.peers.Len()
	t.chunks.Snapshot(&ret)

	// TODO: these can be moved to the connections directly.
	// moving it will reduce the need to iterate the connections
	// to compute the stats.
	ret.MaximumAllowedPeers = t.config.EstablishedConnsPerTorrent
	ret.TotalPeers = t.numTotalPeers()
	ret.ConnectedSeeders = 0
	for _, c := range t.conns.list() {
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

	for _, c := range t.conns.list() {
		if c == nil {
			continue
		}

		ra := c.conn.RemoteAddr()
		if ra == nil {
			// It's been closed and doesn't support RemoteAddr.
			continue
		}
		peers[ra.String()] = struct{}{}
	}

	t._halfOpenmu.RLock()
	for addr := range t.halfOpen {
		peers[addr] = struct{}{}
	}
	t._halfOpenmu.RUnlock()

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
	var (
		dropping []*connection
	)

	if t.closed.IsSet() {
		return errors.New("torrent closed")
	}

	t.lock()
	defer t.unlock()

	for _, c0 := range t.conns.list() {
		if c.PeerID != c0.PeerID {
			continue
		}

		if !t.config.dropDuplicatePeerIds {
			continue
		}

		if left, ok := c.hasPreferredNetworkOver(c0); ok && left {
			dropping = append(dropping, c0)
		} else {
			return errors.New("existing connection preferred")
		}
	}

	if t.conns.length() >= t.maxEstablishedConns {
		c := t.worstBadConn()
		if c == nil {
			return errors.New("don't want conns")
		}

		dropping = append(dropping, c)
	}

	t.conns.insert(c)
	t.pex.added(c)

	t.unlock()
	defer t.lock()

	for _, d := range dropping {
		t.dropConnection(d)
	}

	metrics.Add("added connections", 1)
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

	if t.conns.length() >= t.maxEstablishedConns {
		return false
	}

	return true
}

func (t *torrent) SetMaxEstablishedConns(max int) (oldMax int) {
	oldMax = t.maxEstablishedConns
	t.maxEstablishedConns = max

	cset := t.conns.list()
	wcs := slices.HeapInterface(cset, worseConn)
	for len(cset) > t.maxEstablishedConns && wcs.Len() > 0 {
		t.dropConnection(wcs.Pop().(*connection))
	}
	t.openNewConns()
	return oldMax
}

// func (t *torrent) pieceHashed(piece pieceIndex, failure error) error {
// 	correct := failure == nil
// 	t.config.debug().Printf("hashed piece %d passed=%t", piece, correct)

// 	p := t.piece(piece)
// 	p.numVerifies++

// 	if t.closed.IsSet() {
// 		return failure
// 	}

// 	// Don't score the first time a piece is hashed, it could be an
// 	// initial check.
// 	if p.storageCompletionOk {
// 		if correct {
// 			pieceHashedCorrect.Add(1)
// 		} else {
// 			pieceHashedNotCorrect.Add(1)
// 		}
// 	}

// 	if correct {
// 		// Don't increment stats above connection-level for every involved
// 		// connection.
// 		t.allStats((*ConnStats).incrementPiecesDirtiedGood)

// 		if err := p.Storage().MarkComplete(); err != nil {
// 			t.chunks.ChunksPend(piece)
// 			t.chunks.ChunksRelease(piece)
// 			return errors.Wrapf(err, "%T: error marking piece complete %d: %T - %s", t.storage, piece, err, err)
// 		}
// 		t.chunks.Complete(piece)
// 	} else {
// 		t.chunks.ChunksFailed(piece)

// 		t.allStats((*ConnStats).incrementPiecesDirtiedBad)

// 		p.Storage().MarkNotComplete()
// 		t.onIncompletePiece(piece)
// 	}

// 	return failure
// }

func (t *torrent) cancelRequestsForPiece(piece pieceIndex) {
	t.rLock()
	defer t.rUnlock()
	// TODO: Make faster
	for _, cn := range t.conns.list() {
		cn.updateRequests()
	}
}

// Start the process of connecting to the given peer for the given torrent if
// appropriate.
func (t *torrent) initiateConn(ctx context.Context, peer Peer) {
	if peer.ID == t.cln.peerID {
		return
	}

	addr := IpPort{IP: peer.IP, Port: uint16(peer.Port)}
	if t.addrActive(addr.String()) {
		return
	}

	t._halfOpenmu.Lock()
	t.halfOpen[addr.String()] = peer
	t._halfOpenmu.Unlock()

	go t.cln.outgoingConnection(ctx, t, addr, peer.Source, peer.Trusted)
}

func (t *torrent) noLongerHalfOpen(addr string) {
	t.lock()
	t.dropHalfOpen(addr)
	t.unlock()

	t.openNewConns()
}

// All stats that include this Torrent. Useful when we want to increment
// ConnStats but not for every connection.
func (t *torrent) allStats(f func(*ConnStats)) {
	f(&t.stats)
	f(&t.cln.stats)
}

func (t *torrent) dialTimeout() time.Duration {
	t.rLock()
	defer t.rUnlock()
	return reducedDialTimeout(t.config.MinDialTimeout, t.config.NominalDialTimeout, t.config.HalfOpenConnsPerTorrent, t.peers.Len())
}

func (t *torrent) piece(i int) *metainfo.Piece {
	if t.info == nil {
		return nil
	}

	tmp := t.info.Piece(i)
	return &tmp
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

// // Returns the state of pieces of the torrent. They are grouped into runs of
// // same state. The sum of the state run lengths is the number of pieces
// // in the torrent.
// func (t *torrent) PieceStateRuns() []PieceStateRun {
// 	t.rLock()
// 	defer t.rUnlock()
// 	return t.pieceStateRuns()
// }

// func (t *torrent) PieceState(piece pieceIndex) PieceState {
// 	t.rLock()
// 	defer t.rUnlock()
// 	return t.pieceState(piece)
// }

// // The number of pieces in the torrent. This requires that the info has been
// // obtained first.
// func (t *torrent) NumPieces() pieceIndex {
// 	return t.numPieces()
// }

// // Get missing bytes count for specific piece.
// func (t *torrent) PieceBytesMissing(piece int) int64 {
// 	t.lock()
// 	defer t.unlock()

// 	return int64(t.pieces[piece].bytesLeft())
// }

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
	return t.info.TotalLength()
}

// Returns a run-time generated metainfo for the torrent that includes the
// info bytes and announce-list as currently known to the client.
func (t *torrent) Metainfo() metainfo.MetaInfo {
	t.lock()
	defer t.unlock()
	dup := t.metainfo
	return dup
}

func (t *torrent) initFiles() {
	var offset int64
	for _, fi := range t.info.UpvertedFiles() {
		var path []string
		if len(fi.PathUTF8) != 0 {
			path = fi.PathUTF8
		} else {
			path = fi.Path
		}
		t.files = append(t.files, &File{
			t,
			strings.Join(append([]string{t.info.Name}, path...), "/"),
			offset,
			fi.Length,
			fi,
			// PiecePriorityNone,
		})
		offset += fi.Length
	}
}

// Returns handles to the files in the torrent. This requires that the Info is
// available first.
func (t *torrent) Files() []*File {
	return t.files
}

func (t *torrent) AddPeers(pp []Peer) {
	t.lock()
	defer t.unlock()
	t.addPeers(pp)
}

// // Marks the entire torrent for download. Requires the info first, see
// // GotInfo. Sets piece priorities for historical reasons.
// func (t *torrent) DownloadAll() {
// 	t.DownloadPieces(0, t.numPieces())
// }

func (t *torrent) String() string {
	if s := t.name(); s != "" {
		return strconv.Quote(s)
	}

	return t.infoHash.HexString()
}

// func (t *torrent) Piece(i pieceIndex) *Piece {
// 	return t.piece(i)
// }

func (t *torrent) ping(addr net.UDPAddr) {
	t.cln.eachDhtServer(func(s *dht.Server) {
		go func() {
			ret := dht.Ping3S(context.Background(), s, dht.NewAddr(&addr), s.ID())
			if errorsx.Ignore(ret.Err, context.DeadlineExceeded) != nil {
				log.Println("failed to ping address", ret.Err)
			}
		}()
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
		c.allStats(add(1, func(cs *ConnStats) *count { return &cs.MetadataChunksRead }))
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
