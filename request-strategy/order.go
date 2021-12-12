package request_strategy

import (
	"bytes"
	"expvar"

	"github.com/anacrolix/multiless"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/google/btree"

	"github.com/anacrolix/torrent/types"
)

type (
	RequestIndex  = uint32
	ChunkIndex    = uint32
	Request       = types.Request
	pieceIndex    = types.PieceIndex
	piecePriority = types.PiecePriority
	// This can be made into a type-param later, will be great for testing.
	ChunkSpec = types.ChunkSpec
)

type pieceOrderInput struct {
	PieceRequestOrderState
	PieceRequestOrderKey
}

func pieceOrderLess(i, j pieceOrderInput) multiless.Computation {
	return multiless.New().Int(
		int(j.Priority), int(i.Priority),
	).Bool(
		j.Partial, i.Partial,
	).Int64(
		i.Availability, j.Availability,
	).Int(
		i.Index, j.Index,
	).Lazy(func() multiless.Computation {
		return multiless.New().Cmp(bytes.Compare(
			i.InfoHash[:],
			j.InfoHash[:],
		))
	})
}

var packageExpvarMap = expvar.NewMap("request-strategy")

// Calls f with requestable pieces in order.
func GetRequestablePieces(input Input, pro *PieceRequestOrder, f func(ih metainfo.Hash, pieceIndex int)) {
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl. A nil value means no capacity limit.
	var storageLeft *int64
	if cap, ok := input.Capacity(); ok {
		storageLeft = &cap
	}
	var allTorrentsUnverifiedBytes int64
	pro.tree.Ascend(func(i btree.Item) bool {
		_i := i.(*pieceRequestOrderItem)
		ih := _i.key.InfoHash
		var t Torrent = input.Torrent(ih)
		var piece Piece = t.Piece(_i.key.Index)
		pieceLength := t.PieceLength()
		if storageLeft != nil {
			if *storageLeft < pieceLength {
				return false
			}
			*storageLeft -= pieceLength
		}
		if !piece.Request() || piece.NumPendingChunks() == 0 {
			// TODO: Clarify exactly what is verified. Stuff that's being hashed should be
			// considered unverified and hold up further requests.
			return true
		}
		if input.MaxUnverifiedBytes() != 0 && allTorrentsUnverifiedBytes+pieceLength > input.MaxUnverifiedBytes() {
			return true
		}
		allTorrentsUnverifiedBytes += pieceLength
		f(ih, _i.key.Index)
		return true
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
