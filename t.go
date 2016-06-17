package torrent

import (
	"fmt"
	"strings"
	"time"

	"github.com/anacrolix/missinggo/bitmap"
	"github.com/anacrolix/missinggo/pubsub"

	"github.com/anacrolix/torrent/metainfo"
)

// The torrent's infohash. This is fixed and cannot change. It uniquely
// identifies a torrent.
func (t *Torrent) InfoHash() metainfo.Hash {
	return t.infoHash
}

// Returns a channel that is closed when the info (.Info()) for the torrent
// has become available.
func (t *Torrent) GotInfo() <-chan struct{} {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.gotMetainfo.C()
}

// Returns the metainfo info dictionary, or nil if it's not yet available.
func (t *Torrent) Info() *metainfo.InfoEx {
	return t.info
}

// Returns a Reader bound to the torrent's data. All read calls block until
// the data requested is actually available.
func (t *Torrent) NewReader() (ret *Reader) {
	ret = &Reader{
		t:         t,
		readahead: 5 * 1024 * 1024,
	}
	t.addReader(ret)
	return
}

// Returns the state of pieces of the torrent. They are grouped into runs of
// same state. The sum of the state run lengths is the number of pieces
// in the torrent.
func (t *Torrent) PieceStateRuns() []PieceStateRun {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.pieceStateRuns()
}

func (t *Torrent) PieceState(piece int) PieceState {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.pieceState(piece)
}

// The number of pieces in the torrent. This requires that the info has been
// obtained first.
func (t *Torrent) NumPieces() int {
	return t.numPieces()
}

// Drop the torrent from the client, and close it. It's always safe to do
// this. No data corruption can, or should occur to either the torrent's data,
// or connected peers.
func (t *Torrent) Drop() {
	t.cl.mu.Lock()
	t.cl.dropTorrent(t.infoHash)
	t.cl.mu.Unlock()
}

// Number of bytes of the entire torrent we have completed.
func (t *Torrent) BytesCompleted() int64 {
	t.cl.mu.RLock()
	defer t.cl.mu.RUnlock()
	return t.bytesCompleted()
}

// The subscription emits as (int) the index of pieces as their state changes.
// A state change is when the PieceState for a piece alters in value.
func (t *Torrent) SubscribePieceStateChanges() *pubsub.Subscription {
	return t.pieceStateChanges.Subscribe()
}

// Returns true if the torrent is currently being seeded. This occurs when the
// client is willing to upload without wanting anything in return.
func (t *Torrent) Seeding() bool {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.seeding()
}

func (t *Torrent) Check() bool {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.checking
}

func (t *Torrent) Stop() {
	t.closed.Set()
}

func (t *Torrent) Wait() <-chan struct{} {
	return t.closed.LockedChan(&t.cl.mu)
}

// Clobbers the torrent display name. The display name is used as the torrent
// name if the metainfo is not available.
func (t *Torrent) SetDisplayName(dn string) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	t.setDisplayName(dn)
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *Torrent) Name() string {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.name()
}

// The completed length of all the torrent data, in all its files. This is
// derived from the torrent info, when it is available.
func (t *Torrent) Length() int64 {
	if t.info == nil {
		panic("not valid until info obtained")
	}
	return t.length
}

// Returns a run-time generated metainfo for the torrent that includes the
// info bytes and announce-list as currently known to the client.
func (t *Torrent) Metainfo() *metainfo.MetaInfo {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.newMetaInfo()
}

func (t *Torrent) addReader(r *Reader) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	if t.readers == nil {
		t.readers = make(map[*Reader]struct{})
	}
	t.readers[r] = struct{}{}
	t.readersChanged()
}

func (t *Torrent) deleteReader(r *Reader) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	delete(t.readers, r)
	t.readersChanged()
}

func (t *Torrent) DownloadPieces(begin, end int) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	t.pendPieceRange(begin, end)
}

func (t *Torrent) CancelPieces(begin, end int) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	t.unpendPieceRange(begin, end)
}

// Returns handles to the files in the torrent. This requires the metainfo is
// available first.
func (t *Torrent) Files() (ret []File) {
	t.cl.mu.Lock()
	info := t.Info()
	t.cl.mu.Unlock()
	if info == nil {
		return
	}
	var offset int64
	for _, fi := range info.UpvertedFiles() {
		ret = append(ret, File{
			t,
			strings.Join(append([]string{info.Name}, fi.Path...), "/"),
			offset,
			fi.Length,
			fi,
		})
		offset += fi.Length
	}
	return
}

func (t *Torrent) AddPeers(pp []Peer) {
	cl := t.cl
	cl.mu.Lock()
	defer cl.mu.Unlock()
	t.addPeers(pp)
}

// Marks the entire torrent for download. Requires the info first, see
// GotInfo.
func (t *Torrent) DownloadAll() {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	t.pendPieceRange(0, t.numPieces())
}

func (t *Torrent) String() string {
	s := t.name()
	if s == "" {
		s = fmt.Sprintf("%x", t.infoHash)
	}
	return s
}

func (t *Torrent) AddTrackers(announceList [][]string) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	t.addTrackers(announceList)
}

func (t *Torrent) RemoveTracker(addr string) {
	nn := make([][]string, 0)

	for _, tier := range t.metainfo.AnnounceList {
		n := make([]string, 0)
		for _, tracker := range tier {
			if tracker != addr {
				n = append(n, tracker)
			} else {
				if trackerScraper, ok := t.trackerAnnouncers[tracker]; ok {
					trackerScraper.stop.Set()
				}
			}
		}
		if len(n) > 0 {
			nn = append(nn, n)
		}
	}

	t.metainfo.AnnounceList = nn
}

type Tracker struct {
	Url          string
	Peers        int
	Err          string
	LastAnnounce int64
	NextAnnounce int64
}

func (t *Torrent) Trackers() []Tracker {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	var tt []Tracker

	for _, tier := range t.announceList() {
		for _, trackerURL := range tier {
			T := Tracker{Url: trackerURL}
			if trackerScraper, ok := t.trackerAnnouncers[trackerURL]; ok {
				T.Peers = trackerScraper.peers
				T.Err = trackerScraper.err
				T.LastAnnounce = trackerScraper.lastAnnounce
				T.NextAnnounce = trackerScraper.nextAnnounce
			}
			tt = append(tt, T)
		}
	}

	tt = append(tt, Tracker{"DHT", t.peersDHT, t.errDHT, t.lastAnnounceDHT, 0})

	tt = append(tt, Tracker{"PEX", t.peersPEX, "", 0, 0})

	return tt
}

type PeerInfo struct {
	Id     [20]byte
	Name   string
	Addr   string
	Source peerSource
	// Peer is known to support encryption.
	SupportsEncryption bool
	PiecesCompleted    int
	// byte info information
	Downloaded int64
	Uploaded   int64
}

func (t *Torrent) PeersCount() int {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return len(t.peers)
}

func (t *Torrent) Peers() []PeerInfo {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	v := make([]PeerInfo, 0, len(t.conns))
	for _, value := range t.conns {
		v = append(v, PeerInfo{value.PeerID, value.PeerClientName,
			value.remoteAddr().String(), value.Discovery, value.encrypted,
			value.peerCompleted, value.Downloaded, value.Uploaded})
	}
	return v
}

func (t *Torrent) PiecePended(i int) bool {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	return t.pendingPieces.Contains(i)
}

func (t *Torrent) FileCheck(p int) bool {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.checks[p]
}

func (t *Torrent) FileSetCheck(p int, b bool) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	t.checks[p] = b

	t.fileUpdateCheck()
}

func (t *Torrent) FileUpdateCheck() {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	t.fileUpdateCheck()
}

func (t *Torrent) filePendingBitmap() *bitmap.Bitmap {
	var bm bitmap.Bitmap

	info := t.Info()

	var offset int64
	for i, fi := range info.UpvertedFiles() {
		s := offset / info.PieceLength
		e := (offset+fi.Length)/info.PieceLength + 1
		if t.checks[i] {
			bm.AddRange(int(s), int(e))
		}
		offset += fi.Length
	}

	return &bm
}

func (t *Torrent) fileUpdateCheck() {
	seeding := false
	downloading := false

	if t.cl.activeTorrent(t) {
		if t.pendingBytesCompleted(&t.pendingPieces) >= t.pendingBytesLength(&t.pendingPieces) {
			seeding = true
		} else {
			downloading = true
		}
	}

	t.pendingPieces.Clear()
	t.updatePiecePriorities()

	fb := t.filePendingBitmap()
	fb.IterTyped(func(piece int) (more bool) {
		t.pendPiece(piece)
		return true
	})

	now := time.Now().Unix()

	if t.pendingBytesCompleted(fb) < t.pendingBytesLength(fb) {
		// now we downloading
		t.completedDate = 0
		// did we seed before? update seed timer
		if seeding {
			t.seedingTime = t.seedingTime + (now - t.activateDate)
			t.activateDate = now
		}
	} else {
		// now we seeing

		// did we download before? update downloading timer then
		if downloading {
			t.downloadingTime = t.downloadingTime + (now - t.activateDate)
			t.activateDate = now
		}
	}

	t.updatePiecePriorities()
}

func (t *Torrent) PieceBytesCompleted(i int) int64 {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	p := t.pieces[i]
	return int64(p.length() - p.bytesLeft())
}

func (t *Torrent) PieceLength(i int) int64 {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return int64(t.pieces[i].length().Int())
}

func (t *Torrent) PendingBytesLength() int64 {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	fb := t.filePendingBitmap()

	return t.pendingBytesLength(fb)
}

func (t *Torrent) pendingBytesLength(fb *bitmap.Bitmap) int64 {
	var b int64

	fb.IterTyped(func(piece int) (again bool) {
		p := t.pieces[piece]
		b += int64(p.length())
		return true
	})

	return b
}

func (t *Torrent) PendingBytesCompleted() int64 {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	fb := t.filePendingBitmap()

	return t.pendingBytesCompleted(fb)
}

func (t *Torrent) pendingBytesCompleted(fb *bitmap.Bitmap) int64 {
	var b int64

	fb.IterTyped(func(piece int) (again bool) {
		p := t.pieces[piece]
		b += int64(p.length() - p.bytesLeft())
		return true
	})

	return b
}

func (t *Torrent) Stats() (downloaded int64, uploaded int64, downloading int64, seeding int64) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()

	downloaded = t.downloaded
	uploaded = t.uploaded
	downloading = t.downloadingTime
	seeding = t.seedingTime

	if t.cl.activeTorrent(t) {
		now := time.Now().Unix()
		if t.seeding() {
			seeding = seeding + (now - t.activateDate)
		} else {
			downloading = downloading + (now - t.activateDate)
		}
	}
	return
}

func (t *Torrent) Dates() (added int64, completed int64) {
	added = t.addedDate
	completed = t.completedDate
	return
}
