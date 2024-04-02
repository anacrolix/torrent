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
	pieceIndex    = types.PieceIndex
	piecePriority = types.PiecePriority
	// This can be made into a type-param later, will be great for testing.
	ChunkSpec = types.ChunkSpec
)

func pieceOrderLess(i, j *pieceRequestOrderItem) multiless.Computation {
	return multiless.New().Int(
		int(j.state.Priority), int(i.state.Priority),
		// TODO: Should we match on complete here to prevent churn when availability changes?
	).Bool(
		j.state.Partial, i.state.Partial,
	).Int(
		// If this is done with relative availability, do we lose some determinism? If completeness
		// is used, would that push this far enough down?
		i.state.Availability, j.state.Availability,
	).Int(
		i.key.Index, j.key.Index,
	).Lazy(func() multiless.Computation {
		return multiless.New().Cmp(bytes.Compare(
			i.key.InfoHash[:],
			j.key.InfoHash[:],
		))
	})
}

// Calls f with requestable pieces in order.
func GetRequestablePieces(
	input Input, pro *PieceRequestOrder,
	// Returns true if the piece should be considered against the unverified bytes limit.
	requestPiece func(ih metainfo.Hash, pieceIndex int, orderState PieceRequestOrderState) bool,
) {
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
	pro.tree.Scan(func(item pieceRequestOrderItem) bool {
		ih := item.key.InfoHash
		var t = input.Torrent(ih)
		var piece = t.Piece(item.key.Index)
		pieceLength := t.PieceLength()
		// Storage limits will always apply against requestable pieces, since we need to keep the
		// highest priority pieces, even if they're complete or in an undesirable state.
		if storageLeft != nil {
			if *storageLeft < pieceLength {
				return false
			}
			*storageLeft -= pieceLength
		}
		if piece.Request() {
			if !requestPiece(ih, item.key.Index, item.state) {
				// No blocks are being considered from this piece, so it won't result in unverified
				// bytes.
				return true
			}
		} else if !piece.CountUnverified() {
			// The piece is pristine, and we're not considering it for requests.
			return true
		}
		allTorrentsUnverifiedBytes += pieceLength
		return maxUnverifiedBytes == 0 || allTorrentsUnverifiedBytes < maxUnverifiedBytes
	})
	return
}

type Input interface {
	Torrent(metainfo.Hash) Torrent
	// Storage capacity, shared among all Torrents with the same storage.TorrentCapacity pointer in
	// their storage.Torrent references.
	Capacity() (cap int64, capped bool)
	// Across all the Torrents. This might be partitioned by storage capacity key now.
	MaxUnverifiedBytes() int64
}
