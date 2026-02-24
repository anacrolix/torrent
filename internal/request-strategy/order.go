package requestStrategy

import (
	"bytes"

	"github.com/anacrolix/multiless"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types"
)

type (
	RequestIndex  uint32
	ChunkIndex    = RequestIndex
	Request       = types.Request
	piecePriority = types.PiecePriority
	// This can be made into a type-param later, will be great for testing.
	ChunkSpec = types.ChunkSpec
)

// Piece request ordering factoring in storage limits, user-assigned priority, network availability.
// Is it missing the random piece affinity assigned per torrent? Can we do that deterministically
// per-client?
func pieceOrderLess(i, j *PieceRequestOrderItem) multiless.Computation {
	return multiless.New().Int(
		int(j.State.Priority), int(i.State.Priority),
		// TODO: Should we match on complete here to prevent churn when availability changes? (Answer: Yes).
	).Bool(
		j.State.Partial, i.State.Partial,
	).Int(
		// If this is done with relative availability, do we lose some determinism? If completeness
		// is used, would that push this far enough down? What happens if we have a piece in the
		// order, but it has availability 0?
		i.State.Availability, j.State.Availability,
	).Int(
		i.Key.Index, j.Key.Index,
	).Lazy(func() multiless.Computation {
		a := i.Key.InfoHash.Value()
		b := j.Key.InfoHash.Value()
		return multiless.New().Cmp(bytes.Compare(a[:], b[:]))
	})
}

// This did return true if the piece should be considered against the unverified bytes limit. But
// that would cause thrashing on completion: The order should be stable. This is now a 3-tuple
// iterator.
type RequestPieceFunc func(ih metainfo.Hash, pieceIndex int, orderState PieceRequestOrderState) bool

// Calls f with requestable pieces in order. Returns false if iteration should stop.
func GetRequestablePieces(
	input Input, pro *PieceRequestOrder,
	// Pieces submitted to this callback passed Piece.Request and so are ready for immediate
	// download.
	requestPiece RequestPieceFunc,
) bool {
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl. A nil value means no capacity limit.
	var storageLeft *int64
	if cap, ok := input.Capacity(); ok {
		storageLeft = &cap
	}
	var (
		allTorrentsUnverifiedBytes int64
		maxUnverifiedBytes         = input.MaxUnverifiedBytes()
	)
	for item := range pro.tree.Scan {
		ih := item.Key.InfoHash.Value()
		t := input.Torrent(ih)
		piece := t.Piece(item.Key.Index)
		pieceLength := t.PieceLength()
		// Storage limits will always apply against requestable pieces, since we need to keep the
		// highest priority pieces, even if they're complete or in an undesirable state.
		if storageLeft != nil {
			if *storageLeft < pieceLength {
				break
			}
			*storageLeft -= pieceLength
		}
		if piece.Request() {
			if !requestPiece(ih, item.Key.Index, item.State) {
				// No blocks are being considered from this piece, so it won't result in unverified
				// bytes.
				return false
			}
		} else if !piece.CountUnverified() {
			// The piece is pristine, and we're not considering it for requests.
			continue
		}
		allTorrentsUnverifiedBytes += pieceLength
		if maxUnverifiedBytes != 0 && allTorrentsUnverifiedBytes >= maxUnverifiedBytes {
			break
		}
	}
	return true
}

type Input interface {
	Torrent(metainfo.Hash) Torrent
	// Storage capacity, shared among all Torrents with the same storage.TorrentCapacity pointer in
	// their storage.Torrent references.
	Capacity() (cap int64, capped bool)
	// Across all the Torrents. This might be partitioned by storage capacity key now.
	MaxUnverifiedBytes() int64
}
