package torrent

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anacrolix/chansync/events"
	"github.com/anacrolix/missinggo/v2/pubsub"
	"github.com/anacrolix/sync"
	"golang.org/x/sync/errgroup"

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
	return t.newReader(0, t.length(true))
}

func (t *Torrent) newReader(offset, length int64) Reader {
	r := reader{
		t:      t,
		offset: offset,
		length: length,
	}
	r.readaheadFunc = defaultReadaheadFunc
	t.addReader(&r, true)
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
	return t.numPieces(true)
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
	t.imu.Lock()
	defer t.imu.Unlock()

	if !t.haveInfo(false) {
		t.displayName = dn
	}
}

// The current working name for the torrent. Either the name in the info dict,
// or a display name given such as by the dn value in a magnet link, or "".
func (t *Torrent) Name() string {
	return t.name(true)
}

// The completed length of all the torrent data, in all its files. This is
// derived from the torrent info, when it is available.
func (t *Torrent) Length() int64 {
	return t.length(true)
}

// Returns a run-time generated metainfo for the torrent that includes the
// info bytes and announce-list as currently known to the client.
func (t *Torrent) Metainfo() metainfo.MetaInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.imu.RLock()
	defer t.imu.RUnlock()
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

func (t *Torrent) addReader(r *reader, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}
	if t.readers == nil {
		t.readers = make(map[*reader]struct{})
	}
	t.readers[r] = struct{}{}
	r.posChanged(true, false)
}

func (t *Torrent) deleteReader(r *reader, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	delete(t.readers, r)
	t.readersChanged(false)
}

// Raise the priorities of pieces in the range [begin, end) to at least Normal
// priority. Piece indexes are not the same as bytes. Requires that the info
// has been obtained, see Torrent.Info and Torrent.GotInfo.
func (t *Torrent) DownloadPieces(begin, end pieceIndex) {
	// this used to call t.updatePiecePriority(i, "Torrent.DownloadPieces", false)
	// however that is expensive for large torrents because it calls
	// c.updateRequests(reason, true, false) for each piece which forces an iteration
	// of the piece order tree.  Instead the code below updates the priority with
	// no triggers and then updates the peers at the end of that process

	if t.dataDownloadDisallowed.Bool() {
		return
	}

	t.disallowDataDownload(true)

	name := t.Name()

	mu := sync.RWMutex{}
	changes := map[pieceIndex]struct{}{}
	haveTrigger := false
	failedHashes := 0

	g, ctx := errgroup.WithContext(context.Background())
	_, cancel := context.WithCancel(ctx)
	// this is limited at the moment to avoid exess cpu usage
	// may need to be dynamically set depending on the queue size
	// there is a trade off though against memory usage (at the moment)
	// as if not enough results processors are active memory buffers will
	// grow leading to oom
	g.SetLimit(maxInt(runtime.NumCPU()*4-3, t.cl.config.PieceHashersPerTorrent/2))
	defer cancel()

	fmt.Println("DL", name, "hashers", maxInt(runtime.NumCPU()*4-3, t.cl.config.PieceHashersPerTorrent/2))

	var hashed atomic.Int64
	var complete atomic.Int64
	start := time.Now()
	defer func() {
		fmt.Println("DL", name, "DONE", "C:", complete.Load(), "H:", hashed.Load(), "HR:", float64(hashed.Load())/time.Since(start).Seconds())
	}()

	for i := begin; i < end; i++ {
		i := i

		g.Go(func() error {
			t.mu.RLock()
			piece := &t.pieces[i]
			t.mu.RUnlock()

			piece.Storage()

			completion := piece.completion(true, true)
			storage := piece.Storage()

			if completion.Complete {
				complete.Add(int64(piece.length(true)))
				//fmt.Println("DL complete", complete.Load())
				return nil
			}

			mu.RLock()
			checkCompletion := failedHashes < 8
			mu.RUnlock()

			if checkCompletion && !storage.IsNew() {
				hashed.Add(int64(piece.length(true)))
				//fmt.Println("DL hashed", hashed.Load())
				if sum, _, err := t.hashPiece(piece); err == nil && sum == *piece.hash {
					//storage.MarkComplete(false)
					//t.updatePieceCompletion(i, true)
					return nil
				}

				mu.Lock()
				failedHashes++
				mu.Unlock()
			}

			if piece.priority.Raise(PiecePriorityNormal) {
				pendingChanged := t.updatePiecePriorityNoTriggers(i, true)

				mu.Lock()
				if pendingChanged && !haveTrigger {
					haveTrigger = true
				}
				changes[i] = struct{}{}
				mu.Unlock()
			}

			return nil
		})
	}

	g.Wait()

	if !t.disableTriggers && haveTrigger {
		func() {
			t.mu.Lock()
			defer t.mu.Unlock()

			t.allowDataDownload(false, false)

			t.iterPeers(func(c *Peer) {
				if !c.isLowOnRequests(true, false) {
					return
				}

				for piece := range changes {
					if !c.peerHasPiece(piece, true, false) {
						return
					}
					if ignore := c.requestState.Interested && c.peerChoking && !c.peerAllowedFast.Contains(piece); ignore {
						return
					}
				}

				c.updateRequests("Torrent.DownloadPieces", false)
			}, false)

			t.maybeNewConns(false)
		}()
	} else {
		t.allowDataDownload(true, true)
	}

	for piece := range changes {
		t.publishPieceStateChange(piece, true)
	}
}

func (t *Torrent) CancelPieces(begin, end pieceIndex) {
	for i := begin; i < end; i++ {
		func() {
			// don't lock out all processing between pieces while pieces are updated
			t.mu.Lock()
			defer t.mu.Unlock()
			p := &t.pieces[i]
			if p.priority == PiecePriorityNone {
				return
			}
			p.priority = PiecePriorityNone
			t.updatePiecePriority(i, "Torrent.CancelPieces", false)
		}()
	}
}

func (t *Torrent) initFiles(lock bool) {
	if lock {
		t.imu.RLock()
		defer t.imu.RUnlock()
	}

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
	go t.DownloadPieces(0, t.numPieces(true))
}

func (t *Torrent) String() string {
	s := t.name(true)
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
