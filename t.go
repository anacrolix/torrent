package torrent

import (
	"strconv"
	"strings"

	"github.com/anacrolix/missinggo/pubsub"

	"github.com/anacrolix/torrent/metainfo"
)

// The torrent's infohash. This is fixed and cannot change. It uniquely
// identifies a torrent.
func (t *torrent) InfoHash() metainfo.Hash {
	return t.infoHash
}

// Returns a channel that is closed when the info (.Info()) for the torrent
// has become available.
func (t *torrent) GotInfo() <-chan struct{} {
	t.cl.lock()
	defer t.cl.unlock()
	return t.gotMetainfo.C()
}

// Returns the metainfo info dictionary, or nil if it's not yet available.
func (t *torrent) Info() *metainfo.Info {
	t.cl.lock()
	defer t.cl.unlock()
	return t.info
}

// Returns a Reader bound to the torrent's data. All read calls block until
// the data requested is actually available.
func (t *torrent) NewReader() Reader {
	r := reader{
		mu:        t.cl.locker(),
		t:         t,
		readahead: 5 * 1024 * 1024,
		length:    *t.length,
	}
	t.addReader(&r)
	return &r
}

// Returns the state of pieces of the torrent. They are grouped into runs of
// same state. The sum of the state run lengths is the number of pieces
// in the torrent.
func (t *torrent) PieceStateRuns() []PieceStateRun {
	t.cl.rLock()
	defer t.cl.rUnlock()
	return t.pieceStateRuns()
}

func (t *torrent) PieceState(piece pieceIndex) PieceState {
	t.cl.rLock()
	defer t.cl.rUnlock()
	return t.pieceState(piece)
}

// The number of pieces in the torrent. This requires that the info has been
// obtained first.
func (t *torrent) NumPieces() pieceIndex {
	return t.numPieces()
}

// Get missing bytes count for specific piece.
func (t *torrent) PieceBytesMissing(piece int) int64 {
	t.cl.lock()
	defer t.cl.unlock()

	return int64(t.pieces[piece].bytesLeft())
}

// Drop the torrent from the client, and close it. It's always safe to do
// this. No data corruption can, or should occur to either the torrent's data,
// or connected peers.
func (t *torrent) Drop() {
	t.cl.lock()
	t.cl.dropTorrent(t.infoHash)
	t.cl.unlock()
}

// Number of bytes of the entire torrent we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces. Do not use this
// for download rate, as it can go down when pieces are lost or fail checks.
// Sample Torrent.Stats.DataBytesRead for actual file data download rate.
func (t *torrent) BytesCompleted() int64 {
	t.cl.rLock()
	defer t.cl.rUnlock()
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
	t.cl.lock()
	defer t.cl.unlock()
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
	return *t.length
}

// Returns a run-time generated metainfo for the torrent that includes the
// info bytes and announce-list as currently known to the client.
func (t *torrent) Metainfo() metainfo.MetaInfo {
	t.cl.lock()
	defer t.cl.unlock()
	return t.newMetaInfo()
}

func (t *torrent) addReader(r *reader) {
	t.cl.lock()
	defer t.cl.unlock()
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
	t.cl.lock()
	defer t.cl.unlock()
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
	t.cl.lock()
	defer t.cl.unlock()
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
	cl := t.cl
	cl.lock()
	defer cl.unlock()
	t.addPeers(pp)
}

// Marks the entire torrent for download. Requires the info first, see
// GotInfo. Sets piece priorities for historical reasons.
func (t *torrent) DownloadAll() {
	t.DownloadPieces(0, t.numPieces())
}

func (t *torrent) String() string {
	s := t.name()
	if s == "" {
		return t.infoHash.HexString()
	} else {
		return strconv.Quote(s)
	}
}

func (t *torrent) AddTrackers(announceList [][]string) {
	t.cl.lock()
	defer t.cl.unlock()
	t.addTrackers(announceList)
}

func (t *torrent) Piece(i pieceIndex) *Piece {
	return t.piece(i)
}
