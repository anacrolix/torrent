package torrent

import (
	"github.com/anacrolix/missinggo/pubsub"

	"github.com/anacrolix/torrent/metainfo"
)

// This file contains Torrent, until I decide where the private, lower-case
// "torrent" type belongs. That type is currently mostly in torrent.go.

// The public handle to a live torrent within a Client.
type Torrent struct {
	cl *Client
	*torrent
}

// The torrent's infohash. This is fixed and cannot change. It uniquely
// identifies a torrent.
func (t Torrent) InfoHash() InfoHash {
	return t.torrent.InfoHash
}

// Closed when the info (.Info()) for the torrent has become available. Using
// features of Torrent that require the info before it is available will have
// undefined behaviour.
func (t *Torrent) GotInfo() <-chan struct{} {
	return t.torrent.gotMetainfo
}

// Returns the metainfo, or nil if it's not yet available.
func (t *Torrent) Info() *metainfo.Info {
	return t.torrent.Info
}

// Returns a Reader bound to the torrent's data. All read calls block until
// the data requested is actually available. Priorities are set to ensure the
// data requested will be downloaded as soon as possible.
func (t *Torrent) NewReader() (ret *Reader) {
	ret = &Reader{
		t:         t,
		readahead: 5 * 1024 * 1024,
	}
	return
}

// Returns the state of pieces of the torrent. They are grouped into runs of
// same state. The sum of the state run lengths is the number of pieces
// in the torrent.
func (t *Torrent) PieceStateRuns() []PieceStateRun {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.torrent.pieceStateRuns()
}

func (t Torrent) NumPieces() int {
	return t.numPieces()
}

// Drop the torrent from the client, and close it.
func (t Torrent) Drop() {
	t.cl.mu.Lock()
	t.cl.dropTorrent(t.torrent.InfoHash)
	t.cl.mu.Unlock()
}

// Number of bytes of the entire torrent we have completed.
func (t Torrent) BytesCompleted() int64 {
	t.cl.mu.RLock()
	defer t.cl.mu.RUnlock()
	return t.bytesCompleted()
}

// The subscription emits as (int) the index of pieces as their state changes.
// A state change is when the PieceState for a piece alters in value.
func (t Torrent) SubscribePieceStateChanges() *pubsub.Subscription {
	return t.torrent.pieceStateChanges.Subscribe()
}

// Returns true if the torrent is currently being seeded. This occurs when the
// client is willing to upload without wanting anything in return.
func (t Torrent) Seeding() bool {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	return t.cl.seeding(t.torrent)
}
