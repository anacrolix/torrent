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
	var pieces []Piece
	var numPendingChunks []int

	func() {
		if lockTorrent {
			t.RLock()
			defer t.RUnlock()
		}

		pro := t.GetPieceRequestOrder()

		items = make([]pieceRequestOrderItem, 0, pro.Len())
		pieces = make([]Piece, t.NumPieces())
		numPendingChunks = make([]int, len(pieces))

		pro.Scan(func(_i pieceRequestOrderItem) bool {
			items = append(items, _i)
			pieceIndex := _i.key.Index
			pieces[pieceIndex] = t.Piece(pieceIndex, false)
			numPendingChunks[pieceIndex] = pieces[pieceIndex].NumPendingChunks(false)
			return true
		})
	}()

	for _, _i := range items {
		ih := _i.key.InfoHash

		pieceLength := t.PieceLength()
		if storageLeft != nil {
			if *storageLeft < pieceLength {
				break
			}
			*storageLeft -= pieceLength
		}

		/*piece := pieces[_i.key.Index]*/
		if /*!piece.Request(lockTorrent) ||*/ numPendingChunks[_i.key.Index] == 0 {
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
}

type Input interface {
	Torrent(metainfo.Hash) Torrent
	// Storage capacity, shared among all Torrents with the same storage.TorrentCapacity pointer in
	// their storage.Torrent references.
	Capacity() (cap int64, capped bool)
	// Across all the Torrents. This might be partitioned by storage capacity key now.
	MaxUnverifiedBytes() int64
}
