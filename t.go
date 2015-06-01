package torrent

import (
	"github.com/anacrolix/torrent/metainfo"
)

// The public interface for a torrent within a Client.

// A handle to a live torrent within a Client.
type Torrent struct {
	cl *Client
	*torrent
}

// Closed when the info (.Info()) for the torrent has become available. Using
// features of Torrent that require the info before it is available will have
// undefined behaviour.
func (t *Torrent) GotInfo() <-chan struct{} {
	return t.torrent.gotMetainfo
}

func (t *Torrent) Info() *metainfo.Info {
	return t.torrent.Info
}

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
