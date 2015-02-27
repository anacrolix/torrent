package torrent

import (
	"crypto"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

const (
	pieceHash          = crypto.SHA1
	maxRequests        = 250        // Maximum pending requests we allow peers to send us.
	chunkSize          = 0x4000     // 16KiB
	BEP20              = "-GT0000-" // Peer ID client identifier prefix
	nominalDialTimeout = time.Second * 30
	minDialTimeout     = 5 * time.Second
)

type (
	InfoHash [20]byte
	pieceSum [20]byte
)

func (ih *InfoHash) AsString() string {
	return string(ih[:])
}

func (ih *InfoHash) HexString() string {
	return fmt.Sprintf("%x", ih[:])
}

type piecePriority byte

const (
	piecePriorityNone piecePriority = iota
	piecePriorityNormal
	piecePriorityReadahead
	piecePriorityNext
	piecePriorityNow
)

type piece struct {
	Hash              pieceSum
	complete          bool
	PendingChunkSpecs map[chunkSpec]struct{}
	Hashing           bool
	QueuedForHash     bool
	EverHashed        bool
	Event             sync.Cond
	Priority          piecePriority
}

func (p *piece) shuffledPendingChunkSpecs() (css []chunkSpec) {
	if len(p.PendingChunkSpecs) == 0 {
		return
	}
	css = make([]chunkSpec, 0, len(p.PendingChunkSpecs))
	for cs := range p.PendingChunkSpecs {
		css = append(css, cs)
	}
	if len(css) <= 1 {
		return
	}
	for i := range css {
		j := rand.Intn(i + 1)
		css[i], css[j] = css[j], css[i]
	}
	return
}

func (p *piece) Complete() bool {
	return p.complete
}

func lastChunkSpec(pieceLength peer_protocol.Integer) (cs chunkSpec) {
	cs.Begin = (pieceLength - 1) / chunkSize * chunkSize
	cs.Length = pieceLength - cs.Begin
	return
}

type chunkSpec struct {
	Begin, Length peer_protocol.Integer
}

type request struct {
	Index peer_protocol.Integer
	chunkSpec
}

func newRequest(index, begin, length peer_protocol.Integer) request {
	return request{index, chunkSpec{begin, length}}
}

var (
	// Requested data not yet available.
	ErrDataNotReady = errors.New("data not ready")
)

// The size in bytes of a metadata extension piece.
func metadataPieceSize(totalSize int, piece int) int {
	ret := totalSize - piece*(1<<14)
	if ret > 1<<14 {
		ret = 1 << 14
	}
	return ret
}
