// package types contains types that are used by the request strategy and the torrent package and
// need to be shared between them.
package types

import (
	"fmt"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

type PieceIndex = int

type ChunkSpec struct {
	Begin, Length pp.Integer
}

type Request struct {
	Index pp.Integer
	ChunkSpec
}

func (r Request) String() string {
	return fmt.Sprintf("piece %v, %v bytes at %v", r.Index, r.Length, r.Begin)
}

func (r Request) ToMsg(mt pp.MessageType) pp.Message {
	return pp.Message{
		Type:   mt,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	}
}

// Describes the importance of obtaining a particular piece.
type PiecePriority byte

func (pp *PiecePriority) Raise(maybe PiecePriority) bool {
	if maybe > *pp {
		*pp = maybe
		return true
	}
	return false
}

const (
	PiecePriorityNone      PiecePriority = iota // Not wanted. Must be the zero value.
	PiecePriorityNormal                         // Wanted.
	PiecePriorityHigh                           // Wanted a lot.
	PiecePriorityReadahead                      // May be required soon.
	// Deprecated. Succeeds a piece where a read occurred. This used to prioritize the earlier
	// pieces in the readahead window. The request strategy now does this, so it's no longer needed.
	// There is downstream code that still assumes it's in use.
	PiecePriorityNext
	PiecePriorityNow // A Reader is reading in this piece. Highest urgency.
)
