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
	input Input, t Torrent,
	f func(ih metainfo.Hash, pieceIndex int, orderState PieceRequestOrderState),
	lockTorrent bool,
) {
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl. A nil value means no capacity limit.
	var storageLeft *int64
	if cap, ok := input.Capacity(); ok {
		storageLeft = &cap
	}
	var allTorrentsUnverifiedBytes int64

	var items []pieceRequestOrderItem

	func() {
		if lockTorrent {
			t.RLock()
			defer t.RUnlock()
		}

		pro := t.GetPieceRequestOrder()

		items = make([]pieceRequestOrderItem, 0, pro.Len())

		pro.tree.Scan(func(_i pieceRequestOrderItem) bool {
			items = append(items, _i)
			return true
		})
	}()

	for _, _i := range items {
		ih := _i.key.InfoHash
		var piece Piece = t.Piece(_i.key.Index, lockTorrent)

		pieceLength := t.PieceLength()
		if storageLeft != nil {
			if *storageLeft < pieceLength {
				break
			}
			*storageLeft -= pieceLength
		}

		if /*!piece.Request(lockTorrent) ||*/ piece.NumPendingChunks(lockTorrent) == 0 {
			// TODO: Clarify exactly what is verified. Stuff that's being hashed should be
			// considered unverified and hold up further requests.
			continue
		}

		if input.MaxUnverifiedBytes() != 0 && allTorrentsUnverifiedBytes+pieceLength > input.MaxUnverifiedBytes() {
			break
		}
		allTorrentsUnverifiedBytes += pieceLength
		f(ih, _i.key.Index, _i.state)
	}

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
