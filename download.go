package torrent

import(
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/missinggo/pubsub"
	"github.com/anacrolix/torrent/tracker"
)

type Download interface {
	InfoHash() InfoHash
	GotInfo() <-chan struct{}
	Info() *metainfo.Info
	NewReader() (ret *Reader)
	PieceStateRuns() []PieceStateRun
	NumPieces() int
	Drop()
	BytesCompleted() int64
	SubscribePieceStateChanges() *pubsub.Subscription
	Seeding() bool
	SetDisplayName(dn string)
	Client() *Client
	AddPeers(pp []Peer) error
	DownloadAll()
	Trackers() [][]tracker.Client
}