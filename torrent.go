package torrent

import (
	"container/heap"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/bitmap"
	"github.com/anacrolix/missinggo/itertools"
	"github.com/anacrolix/missinggo/perf"
	"github.com/anacrolix/missinggo/pubsub"
	"github.com/bradfitz/iter"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/tracker"
)

func (t *Torrent) chunkIndexSpec(chunkIndex, piece int) chunkSpec {
	return chunkIndexSpec(chunkIndex, t.pieceLength(piece), t.chunkSize)
}

type peersKey struct {
	IPBytes string
	Port    int
}

// Maintains state of torrent within a Client.
type Torrent struct {
	cl *Client

	closed   missinggo.Event
	infoHash metainfo.Hash
	pieces   []piece
	// Values are the piece indices that changed.
	pieceStateChanges *pubsub.PubSub
	chunkSize         pp.Integer
	// Total length of the torrent in bytes. Stored because it's not O(1) to
	// get this from the info dict.
	length int64

	// The storage to open when the info dict becomes available.
	storageOpener storage.Client
	// Storage for torrent data.
	storage storage.Torrent

	metainfo metainfo.MetaInfo

	// The info dict. nil if we don't have it (yet).
	info *metainfo.InfoEx
	// Active peer connections, running message stream loops.
	conns []*connection
	// Set of addrs to which we're attempting to connect. Connections are
	// half-open until all handshakes are completed.
	halfOpen map[string]struct{}

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known.
	peers          map[peersKey]Peer
	wantPeersEvent missinggo.Event
	// An announcer for each tracker URL.
	trackerAnnouncers map[string]*trackerScraper

	// How many times we've initiated a DHT announce.
	numDHTAnnounces int
	// how many peers we got, not sure if we have to use last value or increment
	peersDHT int
	// error message
	errDHT string
	// last call
	lastAnnounceDHT int64

	// peers come from pex, not sure if we have to use last value or increment
	peersPEX int

	// Name used if the info name isn't available.
	displayName string
	// The bencoded bytes of the info dict.
	metadataBytes []byte
	// Each element corresponds to the 16KiB metadata pieces. If true, we have
	// received that piece.
	metadataCompletedChunks []bool

	// Set when .Info is obtained.
	gotMetainfo missinggo.Event

	readers map[*Reader]struct{}

	pendingPieces   bitmap.Bitmap
	completedPieces bitmap.Bitmap

	connPieceInclinationPool sync.Pool

	// files download checkbox
	checks []bool

	// torrent under file concistency check
	checking bool

	// fired when torrent downloaded
	completed missinggo.Event

	// Since we have to have loadTorrent, we woudl like to keep other statistics like all modern torrent apps does.
	downloaded int64
	uploaded   int64
	// dates in seconds
	addedDate     int64
	completedDate int64

	// date in seconds when torrent been StartTorrent, we measure this value to get downloadingTime && seedingTime
	activateDate int64

	// elapsed in seconds
	downloadingTime int64
	seedingTime     int64

	// .torrent info
	creator   string
	createdOn int64
	comment   string
}

func (t *Torrent) setDisplayName(dn string) {
	if t.haveInfo() {
		return
	}
	t.displayName = dn
}

func (t *Torrent) pieceComplete(piece int) bool {
	return t.completedPieces.Get(piece)
}

func (t *Torrent) pieceCompleteUncached(piece int) bool {
	return t.pieces[piece].Storage().GetIsComplete()
}

func (t *Torrent) numConnsUnchoked() (num int) {
	for _, c := range t.conns {
		if !c.PeerChoked {
			num++
		}
	}
	return
}

// There's a connection to that address already.
func (t *Torrent) addrActive(addr string) bool {
	if _, ok := t.halfOpen[addr]; ok {
		return true
	}
	for _, c := range t.conns {
		if c.remoteAddr().String() == addr {
			return true
		}
	}
	return false
}

func (t *Torrent) worstConns(cl *Client) (wcs *worstConns) {
	wcs = &worstConns{
		c:  make([]*connection, 0, len(t.conns)),
		t:  t,
		cl: cl,
	}
	for _, c := range t.conns {
		if !c.closed.IsSet() {
			wcs.c = append(wcs.c, c)
		}
	}
	return
}

func (t *Torrent) addPeer(p Peer) {
	cl := t.cl
	cl.openNewConns(t)
	if len(t.peers) >= torrentPeersHighWater {
		return
	}
	key := peersKey{string(p.IP), p.Port}
	if _, ok := t.peers[key]; ok {
		return
	}
	t.peers[key] = p
	peersAddedBySource.Add(string(p.Source), 1)
	cl.openNewConns(t)

}

func (t *Torrent) invalidateMetadata() {
	t.metadataBytes = nil
	t.metadataCompletedChunks = nil
	t.info = nil
}

func (t *Torrent) saveMetadataPiece(index int, data []byte) {
	if t.haveInfo() {
		return
	}
	if index >= len(t.metadataCompletedChunks) {
		log.Printf("%s: ignoring metadata piece %d", t, index)
		return
	}
	copy(t.metadataBytes[(1<<14)*index:], data)
	t.metadataCompletedChunks[index] = true
}

func (t *Torrent) metadataPieceCount() int {
	return (len(t.metadataBytes) + (1 << 14) - 1) / (1 << 14)
}

func (t *Torrent) haveMetadataPiece(piece int) bool {
	if t.haveInfo() {
		return (1<<14)*piece < len(t.metadataBytes)
	} else {
		return piece < len(t.metadataCompletedChunks) && t.metadataCompletedChunks[piece]
	}
}

func (t *Torrent) metadataSizeKnown() bool {
	return t.metadataBytes != nil
}

func (t *Torrent) metadataSize() int {
	return len(t.metadataBytes)
}

func infoPieceHashes(info *metainfo.Info) (ret []string) {
	for i := 0; i < len(info.Pieces); i += 20 {
		ret = append(ret, string(info.Pieces[i:i+20]))
	}
	return
}

// Called when metadata for a torrent becomes available.
func (t *Torrent) setInfoBytes(b []byte) error {
	if t.haveInfo() {
		return nil
	}
	err := t.loadInfoBytes(b, nil)
	if err != nil {
		return err
	}
	for _, conn := range t.conns {
		if err = conn.setNumPieces(t.numPieces()); err != nil {
			log.Printf("closing connection: %s", err)
			conn.Close()
		}
	}
	t.check()
	return nil
}

func (t *Torrent) loadInfoBytes(b []byte, checks []bool) error {
	var ie *metainfo.InfoEx
	err := bencode.Unmarshal(b, &ie)
	if err != nil {
		return fmt.Errorf("error unmarshalling info bytes: %s", err)
	}
	if ie.Hash() != t.infoHash {
		return errors.New("info bytes have wrong hash")
	}
	err = validateInfo(&ie.Info)
	if err != nil {
		return fmt.Errorf("bad info: %s", err)
	}
	defer t.updateWantPeersEvent()
	t.info = ie
	t.displayName = "" // Save a few bytes lol.
	t.cl.event.Broadcast()
	t.gotMetainfo.Set()
	t.storage, err = t.storageOpener.OpenTorrent(t.info)
	if err != nil {
		return fmt.Errorf("error opening torrent storage: %s", err)
	}
	t.length = 0
	ff := t.info.UpvertedFiles()
	for _, f := range ff {
		t.length += f.Length
	}
	t.metadataBytes = b
	t.metadataCompletedChunks = nil
	hashes := infoPieceHashes(&t.info.Info)
	t.pieces = make([]piece, len(hashes))
	for i, hash := range hashes {
		piece := &t.pieces[i]
		piece.t = t
		piece.index = i
		piece.noPendingWrites.L = &piece.pendingWritesMutex
		missinggo.CopyExact(piece.Hash[:], hash)
	}
	if checks != nil {
		t.checks = checks
	} else {
		t.checks = make([]bool, len(ff))
		for i := range t.checks {
			t.checks[i] = true
		}
	}
	return nil
}

// check torrent file for concistency. done will not be called if torrent
// stoped.
func (t *Torrent) check() {
	if t.cl.activeTorrent(t) {
		// do not allow run integrity checks on running torrents. this is too time
		// consuming, better redownload parts then check and download at the same
		// time.
		return
	}
	if t.checking {
		return
	}
	t.checking = true
	for i := range t.pieces {
		t.updatePieceCompletion(i)
		p := t.pieces[i]
		// make it EverHashed false, same as torrent freshly added. so other init
		// checks flows in order.
		p.EverHashed = false
		p.QueuedForHash = true
	}
	go func() {
		defer func() {
			t.cl.mu.Lock()
			t.checking = false
			t.cl.mu.Unlock()
		}()
		for i := range t.pieces {
			t.verifyPiece(i)
			if t.closed.IsSet() {
				return
			}
		}
	}()
}

// for big torrent we need to save CPU time and do not recheck files on disk
// bettwen application restarts. loadTorrent allows to restore pieces state
// including: completedPieces, and mark Torrent.pieces as they already been
// checked. Since we allow to skip checks, and user may want to have full
// control over additional checks we need to create Torrent.check() function to
// run manual checks.
func (t *Torrent) loadTorrent(buf []byte, pieces []bool, checks []bool) error {
	err := t.loadInfoBytes(buf, checks)
	if err != nil {
		return err
	}
	// do not run t.check(nil)
	for i := range t.pieces {
		t.updatePieceCompletion(i)
		p := t.pieces[i]
		p.EverHashed = true
		if pieces[i] {
			t.completedPieces.Add(i)
		}
	}
	return nil
}

func (t *Torrent) verifyPiece(piece int) {
	t.cl.verifyPiece(t, piece)
}

func (t *Torrent) haveAllMetadataPieces() bool {
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
func (t *Torrent) setMetadataSize(bytes int64) (err error) {
	if t.haveInfo() {
		// We already know the correct metadata size.
		return
	}
	if bytes <= 0 || bytes > 10000000 { // 10MB, pulled from my ass.
		return errors.New("bad size")
	}
	if t.metadataBytes != nil && len(t.metadataBytes) == int(bytes) {
		return
	}
	t.metadataBytes = make([]byte, bytes)
	t.metadataCompletedChunks = make([]bool, (bytes+(1<<14)-1)/(1<<14))
	for _, c := range t.conns {
		c.requestPendingMetadata()
	}
	return
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *Torrent) name() string {
	if t.haveInfo() {
		return t.info.Name
	}
	str := t.displayName
	// magnets may have incorrect name including '/'
	str = strings.Replace(str, string(os.PathSeparator), string(os.PathListSeparator), -1)
	str = strings.Replace(str, string('|'), string(os.PathListSeparator), -1)
	return str
}

func (t *Torrent) pieceState(index int) (ret PieceState) {
	p := &t.pieces[index]
	ret.Priority = t.piecePriority(index)
	if t.pieceComplete(index) {
		ret.Complete = true
	}
	if p.QueuedForHash || p.Hashing {
		ret.Checking = true
	}
	if !ret.Complete && t.piecePartiallyDownloaded(index) {
		ret.Partial = true
	}
	return
}

func (t *Torrent) metadataPieceSize(piece int) int {
	return metadataPieceSize(len(t.metadataBytes), piece)
}

func (t *Torrent) newMetadataExtensionMessage(c *connection, msgType int, piece int, data []byte) pp.Message {
	d := map[string]int{
		"msg_type": msgType,
		"piece":    piece,
	}
	if data != nil {
		d["total_size"] = len(t.metadataBytes)
	}
	p, err := bencode.Marshal(d)
	if err != nil {
		panic(err)
	}
	return pp.Message{
		Type:            pp.Extended,
		ExtendedID:      c.PeerExtensionIDs["ut_metadata"],
		ExtendedPayload: append(p, data...),
	}
}

func (t *Torrent) pieceStateRuns() (ret []PieceStateRun) {
	rle := missinggo.NewRunLengthEncoder(func(el interface{}, count uint64) {
		ret = append(ret, PieceStateRun{
			PieceState: el.(PieceState),
			Length:     int(count),
		})
	})
	for index := range t.pieces {
		rle.Append(t.pieceState(index), 1)
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
	return
}

func (t *Torrent) writeStatus(w io.Writer, cl *Client) {
	fmt.Fprintf(w, "Infohash: %x\n", t.infoHash)
	fmt.Fprintf(w, "Metadata length: %d\n", t.metadataSize())
	if !t.haveInfo() {
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
	fmt.Fprintf(w, "Piece length: %s\n", func() string {
		if t.haveInfo() {
			return fmt.Sprint(t.usualPieceSize())
		} else {
			return "?"
		}
	}())
	if t.haveInfo() {
		fmt.Fprintf(w, "Num Pieces: %d\n", t.numPieces())
		fmt.Fprint(w, "Piece States:")
		for _, psr := range t.pieceStateRuns() {
			w.Write([]byte(" "))
			w.Write([]byte(pieceStateRunStatusChars(psr)))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Reader Pieces:")
	t.forReaderOffsetPieces(func(begin, end int) (again bool) {
		fmt.Fprintf(w, " %d:%d", begin, end)
		return true
	})
	fmt.Fprintln(w)

	fmt.Fprintf(w, "Trackers: ")
	for _url := range t.trackerAnnouncers {
		fmt.Fprintf(w, "%q ", _url)
	}
	fmt.Fprintf(w, "\n")

	fmt.Fprintf(w, "DHT Announces: %d\n", t.numDHTAnnounces)

	fmt.Fprintf(w, "Pending peers: %d\n", len(t.peers))
	fmt.Fprintf(w, "Half open: %d\n", len(t.halfOpen))
	fmt.Fprintf(w, "Active peers: %d\n", len(t.conns))
	sort.Sort(&worstConns{
		c:  t.conns,
		t:  t,
		cl: cl,
	})
	for i, c := range t.conns {
		fmt.Fprintf(w, "%2d. ", i+1)
		c.WriteStatus(w, t)
	}
}

func (t *Torrent) haveInfo() bool {
	return t.info != nil
}

// TODO: Include URIs that weren't converted to tracker clients.
func (t *Torrent) announceList() (al [][]string) {
	return t.metainfo.AnnounceList
}

// Returns a run-time generated MetaInfo that includes the info bytes and
// announce-list as currently known to the client.
func (t *Torrent) newMetaInfo() (mi *metainfo.MetaInfo) {
	mi = &metainfo.MetaInfo{
		CreationDate: t.createdOn,
		Comment:      t.comment,
		CreatedBy:    t.creator,
		AnnounceList: t.announceList(),
	}
	if t.info != nil {
		mi.Info = *t.info
	}
	return
}

func (t *Torrent) bytesLeft() (left int64) {
	for i := 0; i < t.numPieces(); i++ {
		left += int64(t.pieces[i].bytesLeft())
	}
	return
}

// Bytes left to give in tracker announces.
func (t *Torrent) bytesLeftAnnounce() uint64 {
	if t.haveInfo() {
		return uint64(t.bytesLeft())
	} else {
		return math.MaxUint64
	}
}

func (t *Torrent) piecePartiallyDownloaded(piece int) bool {
	if t.pieceComplete(piece) {
		return false
	}
	if t.pieceAllDirty(piece) {
		return false
	}
	return t.pieces[piece].hasDirtyChunks()
}

func (t *Torrent) usualPieceSize() int {
	return int(t.info.PieceLength)
}

func (t *Torrent) lastPieceSize() int {
	return int(t.pieceLength(t.numPieces() - 1))
}

func (t *Torrent) numPieces() int {
	return t.info.NumPieces()
}

func (t *Torrent) numPiecesCompleted() (num int) {
	return t.completedPieces.Len()
}

func (t *Torrent) close() (err error) {
	t.closed.Set()
	if c, ok := t.storage.(io.Closer); ok {
		c.Close()
	}
	for _, tracker := range t.trackerAnnouncers {
		tracker.stop.Set()
	}
	t.trackerAnnouncers = nil
	for _, conn := range t.conns {
		conn.Close()
	}
	t.conns = nil
	t.peers = nil
	t.halfOpen = nil
	t.pieceStateChanges.Close()
	return
}

func (t *Torrent) requestOffset(r request) int64 {
	return torrentRequestOffset(t.length, int64(t.usualPieceSize()), r)
}

// Return the request that would include the given offset into the torrent
// data. Returns !ok if there is no such request.
func (t *Torrent) offsetRequest(off int64) (req request, ok bool) {
	return torrentOffsetRequest(t.length, t.info.PieceLength, int64(t.chunkSize), off)
}

func (t *Torrent) writeChunk(piece int, begin int64, data []byte) (err error) {
	tr := perf.NewTimer()

	n, err := t.pieces[piece].Storage().WriteAt(data, begin)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		tr.Stop("write chunk")
	}
	return
}

func (t *Torrent) bitfield() (bf []bool) {
	bf = make([]bool, t.numPieces())
	t.completedPieces.IterTyped(func(piece int) (again bool) {
		bf[piece] = true
		return true
	})
	return
}

func (t *Torrent) validOutgoingRequest(r request) bool {
	if r.Index >= pp.Integer(t.info.NumPieces()) {
		return false
	}
	if r.Begin%t.chunkSize != 0 {
		return false
	}
	if r.Length > t.chunkSize {
		return false
	}
	pieceLength := t.pieceLength(int(r.Index))
	if r.Begin+r.Length > pieceLength {
		return false
	}
	return r.Length == t.chunkSize || r.Begin+r.Length == pieceLength
}

func (t *Torrent) pieceChunks(piece int) (css []chunkSpec) {
	css = make([]chunkSpec, 0, (t.pieceLength(piece)+t.chunkSize-1)/t.chunkSize)
	var cs chunkSpec
	for left := t.pieceLength(piece); left != 0; left -= cs.Length {
		cs.Length = left
		if cs.Length > t.chunkSize {
			cs.Length = t.chunkSize
		}
		css = append(css, cs)
		cs.Begin += cs.Length
	}
	return
}

func (t *Torrent) pieceNumChunks(piece int) int {
	return int((t.pieceLength(piece) + t.chunkSize - 1) / t.chunkSize)
}

func (t *Torrent) pendAllChunkSpecs(pieceIndex int) {
	t.pieces[pieceIndex].DirtyChunks.Clear()
}

type Peer struct {
	Id     [20]byte
	IP     net.IP
	Port   int
	Source peerSource
	// Peer is known to support encryption.
	SupportsEncryption bool
}

func (t *Torrent) pieceLength(piece int) (len_ pp.Integer) {
	if piece < 0 || piece >= t.info.NumPieces() {
		return
	}
	if piece == t.numPieces()-1 {
		len_ = pp.Integer(t.length % t.info.PieceLength)
	}
	if len_ == 0 {
		len_ = pp.Integer(t.info.PieceLength)
	}
	return
}

func (t *Torrent) hashPiece(piece int) (ret metainfo.Hash) {
	hash := pieceHash.New()
	p := &t.pieces[piece]
	p.waitNoPendingWrites()
	ip := t.info.Piece(piece)
	pl := ip.Length()
	n, err := io.Copy(hash, io.NewSectionReader(t.pieces[piece].Storage(), 0, pl))
	if n == pl {
		missinggo.CopyExact(&ret, hash.Sum(nil))
		return
	}
	if err != io.ErrUnexpectedEOF && !os.IsNotExist(err) {
		log.Printf("unexpected error hashing piece with %T: %s", t.storage, err)
	}
	return
}

func (t *Torrent) haveAllPieces() bool {
	if !t.haveInfo() {
		return false
	}
	return t.completedPieces.Len() == t.numPieces()
}

func (t *Torrent) haveAnyPieces() bool {
	for i := range t.pieces {
		if t.pieceComplete(i) {
			return true
		}
	}
	return false
}

func (t *Torrent) havePiece(index int) bool {
	return t.haveInfo() && t.pieceComplete(index)
}

func (t *Torrent) haveChunk(r request) (ret bool) {
	// defer func() {
	// 	log.Println("have chunk", r, ret)
	// }()
	if !t.haveInfo() {
		return false
	}
	if t.pieceComplete(int(r.Index)) {
		return true
	}
	p := &t.pieces[r.Index]
	return !p.pendingChunk(r.chunkSpec, t.chunkSize)
}

func chunkIndex(cs chunkSpec, chunkSize pp.Integer) int {
	return int(cs.Begin / chunkSize)
}

func (t *Torrent) wantPiece(r request) bool {
	if !t.wantPieceIndex(int(r.Index)) {
		return false
	}
	if t.pieces[r.Index].pendingChunk(r.chunkSpec, t.chunkSize) {
		return true
	}
	// TODO: What about pieces that were wanted, but aren't now, and aren't
	// completed either? That used to be done here.
	return false
}

func (t *Torrent) wantPieceIndex(index int) bool {
	if !t.haveInfo() {
		return false
	}
	p := &t.pieces[index]
	if p.QueuedForHash {
		return false
	}
	if p.Hashing {
		return false
	}
	if t.pieceComplete(index) {
		return false
	}
	if t.pendingPieces.Contains(index) {
		return true
	}
	return !t.forReaderOffsetPieces(func(begin, end int) bool {
		return index < begin || index >= end
	})
}

func (t *Torrent) forNeededPieces(f func(piece int) (more bool)) (all bool) {
	return t.forReaderOffsetPieces(func(begin, end int) (more bool) {
		for i := begin; begin < end; i++ {
			if !f(i) {
				return false
			}
		}
		return true
	})
}

func (t *Torrent) connHasWantedPieces(c *connection) bool {
	return !c.pieceRequestOrder.IsEmpty()
}

func (t *Torrent) extentPieces(off, _len int64) (pieces []int) {
	for i := off / int64(t.usualPieceSize()); i*int64(t.usualPieceSize()) < off+_len; i++ {
		pieces = append(pieces, int(i))
	}
	return
}

func (t *Torrent) worstBadConn(cl *Client) *connection {
	wcs := t.worstConns(cl)
	heap.Init(wcs)
	for wcs.Len() != 0 {
		c := heap.Pop(wcs).(*connection)
		if c.UnwantedChunksReceived >= 6 && c.UnwantedChunksReceived > c.UsefulChunksReceived {
			return c
		}
		if wcs.Len() >= (socketsPerTorrent+1)/2 {
			// Give connections 1 minute to prove themselves.
			if time.Since(c.completedHandshake) > time.Minute {
				return c
			}
		}
	}
	return nil
}

type PieceStateChange struct {
	Index int
	PieceState
}

func (t *Torrent) publishPieceChange(piece int) {
	cur := t.pieceState(piece)
	p := &t.pieces[piece]
	if cur != p.PublicPieceState {
		p.PublicPieceState = cur
		t.pieceStateChanges.Publish(PieceStateChange{
			piece,
			cur,
		})
	}
}

func (t *Torrent) pieceNumPendingChunks(piece int) int {
	if t.pieceComplete(piece) {
		return 0
	}
	return t.pieceNumChunks(piece) - t.pieces[piece].numDirtyChunks()
}

func (t *Torrent) pieceAllDirty(piece int) bool {
	return t.pieces[piece].DirtyChunks.Len() == t.pieceNumChunks(piece)
}

func (t *Torrent) forUrgentPieces(f func(piece int) (again bool)) (all bool) {
	return t.forReaderOffsetPieces(func(begin, end int) (again bool) {
		if begin < end {
			if !f(begin) {
				return false
			}
		}
		return true
	})
}

func (t *Torrent) readersChanged() {
	t.updatePiecePriorities()
}

func (t *Torrent) maybeNewConns() {
	// Tickle the accept routine.
	t.cl.event.Broadcast()
	t.openNewConns()
}

func (t *Torrent) piecePriorityChanged(piece int) {
	for _, c := range t.conns {
		c.updatePiecePriority(piece)
	}
	t.maybeNewConns()
	t.publishPieceChange(piece)
}

func (t *Torrent) updatePiecePriority(piece int) bool {
	p := &t.pieces[piece]
	newPrio := t.piecePriorityUncached(piece)
	if newPrio == p.priority {
		return false
	}
	p.priority = newPrio
	return true
}

// Update all piece priorities in one hit. This function should have the same
// output as updatePiecePriority, but across all pieces.
func (t *Torrent) updatePiecePriorities() {
	newPrios := make([]piecePriority, t.numPieces())
	t.pendingPieces.IterTyped(func(piece int) (more bool) {
		newPrios[piece] = PiecePriorityNormal
		return true
	})
	t.forReaderOffsetPieces(func(begin, end int) (next bool) {
		if begin < end {
			newPrios[begin].Raise(PiecePriorityNow)
		}
		for i := begin + 1; i < end; i++ {
			newPrios[i].Raise(PiecePriorityReadahead)
		}
		return true
	})
	t.completedPieces.IterTyped(func(piece int) (more bool) {
		newPrios[piece] = PiecePriorityNone
		return true
	})
	for i, prio := range newPrios {
		if prio != t.pieces[i].priority {
			t.pieces[i].priority = prio
			t.piecePriorityChanged(i)
		}
	}
}

func (t *Torrent) byteRegionPieces(off, size int64) (begin, end int) {
	if off >= t.length {
		return
	}
	if off < 0 {
		size += off
		off = 0
	}
	if size <= 0 {
		return
	}
	begin = int(off / t.info.PieceLength)
	end = int((off + size + t.info.PieceLength - 1) / t.info.PieceLength)
	if end > t.info.NumPieces() {
		end = t.info.NumPieces()
	}
	return
}

// Returns true if all iterations complete without breaking.
func (t *Torrent) forReaderOffsetPieces(f func(begin, end int) (more bool)) (all bool) {
	// There's an oppurtunity here to build a map of beginning pieces, and a
	// bitmap of the rest. I wonder if it's worth the allocation overhead.
	for r := range t.readers {
		r.mu.Lock()
		pos, readahead := r.pos, r.readahead
		r.mu.Unlock()
		if readahead < 1 {
			readahead = 1
		}
		begin, end := t.byteRegionPieces(pos, readahead)
		if begin >= end {
			continue
		}
		if !f(begin, end) {
			return false
		}
	}
	return true
}

func (t *Torrent) piecePriority(piece int) piecePriority {
	if !t.haveInfo() {
		return PiecePriorityNone
	}
	return t.pieces[piece].priority
}

func (t *Torrent) piecePriorityUncached(piece int) (ret piecePriority) {
	ret = PiecePriorityNone
	if t.pieceComplete(piece) {
		return
	}
	if t.pendingPieces.Contains(piece) {
		ret = PiecePriorityNormal
	}
	raiseRet := ret.Raise
	t.forReaderOffsetPieces(func(begin, end int) (again bool) {
		if piece == begin {
			raiseRet(PiecePriorityNow)
		}
		if begin <= piece && piece < end {
			raiseRet(PiecePriorityReadahead)
		}
		return true
	})
	return
}

func (t *Torrent) pendPiece(piece int) {
	if t.pendingPieces.Contains(piece) {
		return
	}
	if t.havePiece(piece) {
		return
	}
	t.pendingPieces.Add(piece)
	if !t.updatePiecePriority(piece) {
		return
	}
	t.piecePriorityChanged(piece)
}

func (t *Torrent) unpendPiece(piece int) {
	if !t.pendingPieces.Contains(piece) {
		return
	}
	if t.havePiece(piece) {
		return
	}
	t.pendingPieces.Remove(piece)
	if !t.updatePiecePriority(piece) {
		return
	}
	t.piecePriorityChanged(piece)
}

func (t *Torrent) getCompletedPieces() (ret bitmap.Bitmap) {
	return t.completedPieces.Copy()
}

func (t *Torrent) unpendPieces(unpend *bitmap.Bitmap) {
	t.pendingPieces.Sub(unpend)
	t.updatePiecePriorities()
}

func (t *Torrent) pendPieceRange(begin, end int) {
	for i := begin; i < end; i++ {
		t.pendPiece(i)
	}
}

func (t *Torrent) unpendPieceRange(begin, end int) {
	var bm bitmap.Bitmap
	bm.AddRange(begin, end)
	t.unpendPieces(&bm)
}

func (t *Torrent) connRequestPiecePendingChunks(c *connection, piece int) (more bool) {
	if !c.PeerHasPiece(piece) {
		return true
	}
	chunkIndices := t.pieces[piece].undirtiedChunkIndices().ToSortedSlice()
	return itertools.ForPerm(len(chunkIndices), func(i int) bool {
		req := request{pp.Integer(piece), t.chunkIndexSpec(chunkIndices[i], piece)}
		return c.Request(req)
	})
}

func (t *Torrent) pendRequest(req request) {
	ci := chunkIndex(req.chunkSpec, t.chunkSize)
	t.pieces[req.Index].pendChunkIndex(ci)
}

func (t *Torrent) pieceChanged(piece int) {
	t.cl.pieceChanged(t, piece)
}

func (t *Torrent) openNewConns() {
	t.cl.openNewConns(t)
}

func (t *Torrent) getConnPieceInclination() []int {
	_ret := t.connPieceInclinationPool.Get()
	if _ret == nil {
		pieceInclinationsNew.Add(1)
		return rand.Perm(t.numPieces())
	}
	pieceInclinationsReused.Add(1)
	return _ret.([]int)
}

func (t *Torrent) putPieceInclination(pi []int) {
	t.connPieceInclinationPool.Put(pi)
	pieceInclinationsPut.Add(1)
}

func (t *Torrent) updatePieceCompletion(piece int) {
	pcu := t.pieceCompleteUncached(piece)
	changed := t.completedPieces.Get(piece) != pcu
	t.completedPieces.Set(piece, pcu)
	if changed {
		t.pieceChanged(piece)
	}
}

// Non-blocking read. Client lock is not required.
func (t *Torrent) readAt(b []byte, off int64) (n int, err error) {
	p := &t.pieces[off/t.info.PieceLength]
	p.waitNoPendingWrites()
	return p.Storage().ReadAt(b, off-p.Info().Offset())
}

func (t *Torrent) updateAllPieceCompletions() {
	for i := range iter.N(t.numPieces()) {
		t.updatePieceCompletion(i)
	}
}

func (t *Torrent) maybeMetadataCompleted() {
	if t.haveInfo() {
		// Nothing to do.
		return
	}
	if !t.haveAllMetadataPieces() {
		// Don't have enough metadata pieces.
		return
	}
	// TODO(anacrolix): If this fails, I think something harsher should be
	// done.
	err := t.setInfoBytes(t.metadataBytes)
	if err != nil {
		log.Printf("error setting metadata: %s", err)
		t.invalidateMetadata()
		return
	}

	now := time.Now().Unix()
	t.downloadingTime = t.downloadingTime + (now - t.activateDate)
	t.activateDate = now

	t.fileUpdateCheck()

	if t.cl.config.Debug {
		log.Printf("%s: got metadata from peers", t)
	}
}

func (t *Torrent) readerPieces() (ret bitmap.Bitmap) {
	t.forReaderOffsetPieces(func(begin, end int) bool {
		ret.AddRange(begin, end)
		return true
	})
	return
}

func (t *Torrent) needData() bool {
	if !t.haveInfo() {
		return true
	}
	if t.pendingPieces.Len() != 0 {
		return true
	}
	return !t.readerPieces().IterTyped(func(piece int) bool {
		return t.pieceComplete(piece)
	})
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

func (t *Torrent) addTrackers(announceList [][]string) {
	fullAnnounceList := &t.metainfo.AnnounceList
	t.metainfo.AnnounceList = appendMissingTrackerTiers(*fullAnnounceList, len(announceList))
	for tierIndex, trackerURLs := range announceList {
		(*fullAnnounceList)[tierIndex] = appendMissingStrings((*fullAnnounceList)[tierIndex], trackerURLs)
	}
	t.startMissingTrackerScrapers()
	t.updateWantPeersEvent()
}

// Don't call this before the info is available.
func (t *Torrent) bytesCompleted() int64 {
	if !t.haveInfo() {
		return 0
	}
	return t.info.TotalLength() - t.bytesLeft()
}

func (t *Torrent) SetInfoBytes(b []byte) (err error) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.setInfoBytes(b)
}

// Returns true if connection is removed from torrent.Conns.
func (t *Torrent) deleteConnection(c *connection) bool {
	for i0, _c := range t.conns {
		if _c != c {
			continue
		}
		i1 := len(t.conns) - 1
		if i0 != i1 {
			t.conns[i0] = t.conns[i1]
		}
		t.conns = t.conns[:i1]
		return true
	}
	return false
}

func (t *Torrent) dropConnection(c *connection) {
	t.cl.event.Broadcast()
	c.Close()
	if t.deleteConnection(c) {
		t.openNewConns()
	}
}

func (t *Torrent) wantPeers() bool {
	if t.closed.IsSet() {
		return false
	}
	if len(t.peers) > torrentPeersLowWater {
		return false
	}
	return t.needData() || t.seeding()
}

func (t *Torrent) updateWantPeersEvent() {
	if t.wantPeers() {
		t.wantPeersEvent.Set()
	} else {
		t.wantPeersEvent.Clear()
	}
}

// Returns whether the client should make effort to seed the torrent.
func (t *Torrent) seeding() bool {
	cl := t.cl
	if cl.config.NoUpload {
		return false
	}
	if !cl.config.Seed {
		return false
	}
	if t.needData() {
		return false
	}
	return true
}

// Adds and starts tracker scrapers for tracker URLs that aren't already
// running.
func (t *Torrent) startMissingTrackerScrapers() {
	if t.cl.config.DisableTrackers {
		return
	}
	if !t.cl.activeTorrent(t) {
		return
	}
	for _, tier := range t.announceList() {
		for _, trackerURL := range tier {
			if _, ok := t.trackerAnnouncers[trackerURL]; ok {
				continue
			}
			newAnnouncer := &trackerScraper{
				url: trackerURL,
				t:   t,
			}
			if t.trackerAnnouncers == nil {
				t.trackerAnnouncers = make(map[string]*trackerScraper)
			}
			t.trackerAnnouncers[trackerURL] = newAnnouncer
			go newAnnouncer.Run()
		}
	}
}

// Returns an AnnounceRequest with fields filled out to defaults and current
// values.
func (t *Torrent) announceRequest() tracker.AnnounceRequest {
	return tracker.AnnounceRequest{
		Event:    tracker.None,
		NumWant:  -1,
		Port:     uint16(t.cl.incomingPeerPort()),
		PeerId:   t.cl.peerID,
		InfoHash: t.infoHash,
		Left:     t.bytesLeftAnnounce(),
	}
}

func (t *Torrent) announceDHT(impliedPort bool) {
	cl := t.cl
	for {
		select {
		case <-t.wantPeersEvent.LockedChan(&cl.mu):
		case <-t.closed.LockedChan(&cl.mu):
			return
		}
		// log.Printf("getting peers for %q from DHT", t)
		ps, err := cl.dHT.Announce(string(t.infoHash[:]), cl.incomingPeerUDPPort(), impliedPort)
		if err != nil {
			cl.mu.Lock()
			t.errDHT = fmt.Sprintf("error getting peers from dht: %s", err)
			log.Printf(t.errDHT)
			cl.mu.Unlock()
			return
		}
		cl.mu.Lock()
		t.lastAnnounceDHT = time.Now().Unix()
		t.numDHTAnnounces++
		cl.mu.Unlock()
		// Count all the unique addresses we got during this announce.
		allAddrs := make(map[string]struct{})
	getPeers:
		for {
			select {
			case v, ok := <-ps.Peers:
				if !ok {
					break getPeers
				}
				addPeers := make([]Peer, 0, len(v.Peers))
				for _, cp := range v.Peers {
					if cp.Port == 0 {
						// Can't do anything with this.
						continue
					}
					addPeers = append(addPeers, Peer{
						IP:     cp.IP[:],
						Port:   cp.Port,
						Source: peerSourceDHT,
					})
					key := (&net.UDPAddr{
						IP:   cp.IP[:],
						Port: cp.Port,
					}).String()
					allAddrs[key] = struct{}{}
				}
				cl.mu.Lock()
				if t.peers == nil {
					cl.mu.Unlock()
					ps.Close()
					return
				}
				t.addPeers(addPeers)
				numPeers := len(t.peers)
				cl.mu.Unlock()
				if numPeers >= torrentPeersHighWater {
					break getPeers
				}
			case <-t.closed.LockedChan(&cl.mu):
				ps.Close()
				return
			}
		}
		ps.Close()
		cl.mu.Lock()
		t.peersDHT = len(allAddrs)
		cl.mu.Unlock()
		// log.Printf("finished DHT peer scrape for %s: %d peers", t, len(allAddrs))
	}
}

func (t *Torrent) addPeers(peers []Peer) {
	for _, p := range peers {
		if t.cl.badPeerIPPort(p.IP, p.Port) {
			continue
		}
		t.addPeer(p)
	}
}
