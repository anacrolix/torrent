package torrent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anacrolix/chansync/events"
	"github.com/anacrolix/missinggo/v2/pubsub"
	"github.com/anacrolix/sync"

	"github.com/anacrolix/torrent/metainfo"
)

// The Torrent's infohash. This is fixed and cannot change. It uniquely identifies a torrent.
func (t *Torrent) InfoHash() metainfo.Hash {
	return t.infoHash
}

// Returns a channel that is closed when the info (.Info()) for the torrent has become available.
func (t *Torrent) GotInfo() events.Done {
	return t.gotMetainfoC
}

// Returns the metainfo info dictionary, or nil if it's not yet available.
func (t *Torrent) Info() (info *metainfo.Info) {
	t.mu.RLock()
	info = t.info
	t.mu.RUnlock()
	return
}

// Returns a Reader bound to the torrent's data. All read calls block until the data requested is
// actually available. Note that you probably want to ensure the Torrent Info is available first.
func (t *Torrent) NewReader() Reader {
	return t.newReader(0, t.length())
}

func (t *Torrent) newReader(offset, length int64) Reader {
	r := reader{
		mu:     t.cl.locker(),
		t:      t,
		offset: offset,
		length: length,
	}
	r.readaheadFunc = defaultReadaheadFunc
	t.addReader(&r)
	return &r
}

type PieceStateRuns []PieceStateRun

func (me PieceStateRuns) String() (s string) {
	if len(me) > 0 {
		var sb strings.Builder
		sb.WriteString(me[0].String())
		for i := 1; i < len(me); i += 1 {
			sb.WriteByte(' ')
			sb.WriteString(me[i].String())
		}
		return sb.String()
	}
	return
}

// Returns the state of pieces of the torrent. They are grouped into runs of same state. The sum of
// the state run-lengths is the number of pieces in the torrent.
func (t *Torrent) PieceStateRuns() (runs PieceStateRuns) {
	return t.pieceStateRuns(true)
}

func (t *Torrent) PieceState(piece pieceIndex) (ps PieceState) {
	ps = t.pieceState(piece, true)
	return
}

// The number of pieces in the torrent. This requires that the info has been
// obtained first.
func (t *Torrent) NumPieces() pieceIndex {
	return t.numPieces()
}

// Get missing bytes count for specific piece.
func (t *Torrent) PieceBytesMissing(piece int) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return int64(t.pieces[piece].bytesLeft())
}

// Drop the torrent from the client, and close it. It's always safe to do
// this. No data corruption can, or should occur to either the torrent's data,
// or connected peers.
func (t *Torrent) Drop() {
	var wg sync.WaitGroup
	defer wg.Wait()
	t.cl.lock()
	defer t.cl.unlock()
	err := t.cl.dropTorrent(t.infoHash, &wg)
	if err != nil {
		panic(err)
	}
}

// Number of bytes of the entire torrent we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces. Do not use this
// for download rate, as it can go down when pieces are lost or fail checks.
// Sample Torrent.Stats.DataBytesRead for actual file data download rate.
func (t *Torrent) BytesCompleted() int64 {
	return t.bytesCompleted()
}

// The subscription emits as (int) the index of pieces as their state changes.
// A state change is when the PieceState for a piece alters in value.
func (t *Torrent) SubscribePieceStateChanges() *pubsub.Subscription[PieceStateChange] {
	return t.pieceStateChanges.Subscribe()
}

// Returns true if the torrent is currently being seeded. This occurs when the
// client is willing to upload without wanting anything in return.
func (t *Torrent) Seeding() (ret bool) {
	ret = t.seeding(true)
	return
}

// Clobbers the torrent display name if metainfo is unavailable.
// The display name is used as the torrent name while the metainfo is unavailable.
func (t *Torrent) SetDisplayName(dn string) {
	if !t.haveInfo(true) {
		t.mu.Lock()
		t.displayName = dn
		t.mu.Unlock()
	}
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *Torrent) Name() string {
	return t.name()
}

// The completed length of all the torrent data, in all its files. This is
// derived from the torrent info, when it is available.
func (t *Torrent) Length() int64 {
	return t._length.Value
}

// Returns a run-time generated metainfo for the torrent that includes the
// info bytes and announce-list as currently known to the client.
func (t *Torrent) Metainfo() metainfo.MetaInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.newMetaInfo()
}

func (t *Torrent) addReader(r *reader) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.readers == nil {
		t.readers = make(map[*reader]struct{})
	}
	t.readers[r] = struct{}{}
	r.posChanged()
}

func (t *Torrent) deleteReader(r *reader) {
	delete(t.readers, r)
	t.readersChanged()
}

// Raise the priorities of pieces in the range [begin, end) to at least Normal
// priority. Piece indexes are not the same as bytes. Requires that the info
// has been obtained, see Torrent.Info and Torrent.GotInfo.
func (t *Torrent) DownloadPieces(begin, end pieceIndex) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.downloadPiecesLocked(begin, end)
}

func (t *Torrent) downloadPiecesLocked(begin, end pieceIndex) {
	fmt.Println("DPL")
	fmt.Println("DPL", "DONE")
	for i := begin; i < end; i++ {
		if t.pieces[i].priority.Raise(PiecePriorityNormal) {
			t.updatePiecePriority(i, "Torrent.DownloadPieces", false)
		}
	}
}

func (t *Torrent) CancelPieces(begin, end pieceIndex) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelPiecesLocked(begin, end, "Torrent.CancelPieces")
}

func (t *Torrent) cancelPiecesLocked(begin, end pieceIndex, reason string) {
	for i := begin; i < end; i++ {
		p := &t.pieces[i]
		if p.priority == PiecePriorityNone {
			continue
		}
		p.priority = PiecePriorityNone
		t.updatePiecePriority(i, reason, false)
	}
}

func (t *Torrent) initFiles() {
	var offset int64
	t.files = new([]*File)
	for _, fi := range t.info.UpvertedFiles() {
		*t.files = append(*t.files, &File{
			t,
			strings.Join(append([]string{t.info.BestName()}, fi.BestPath()...), "/"),
			offset,
			fi.Length,
			fi,
			fi.DisplayPath(t.info),
			PiecePriorityNone,
		})
		offset += fi.Length
	}
}

// Returns handles to the files in the torrent. This requires that the Info is
// available first.
func (t *Torrent) Files() []*File {
	return *t.files
}

func (t *Torrent) AddPeers(pp []PeerInfo) (n int) {
	n = t.addPeers(pp, true)
	return
}

// Marks the entire torrent for download. Requires the info first, see
// GotInfo. Sets piece priorities for historical reasons.
func (t *Torrent) DownloadAll() {
	t.DownloadPieces(0, t.numPieces())
}

func (t *Torrent) String() string {
	s := t.name()
	if s == "" {
		return t.infoHash.HexString()
	} else {
		return strconv.Quote(s)
	}
}

func (t *Torrent) AddTrackers(announceList [][]string) {
	t.addTrackers(announceList, true)
}

func (t *Torrent) Piece(i pieceIndex) *Piece {
	return t.piece(i, true)
}

func (t *Torrent) PeerConns() []*PeerConn {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ret := make([]*PeerConn, 0, len(t.conns))
	for c := range t.conns {
		ret = append(ret, c)
	}
	return ret
}

func (t *Torrent) WebseedPeerConns() []*Peer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ret := make([]*Peer, 0, len(t.conns))
	for _, c := range t.webSeeds {
		ret = append(ret, c)
	}
	return ret
}
