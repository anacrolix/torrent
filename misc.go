package torrent

import (
	"crypto"
	"errors"
	"fmt"
	"time"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	pieceHash          = crypto.SHA1
	maxRequests        = 250        // Maximum pending requests we allow peers to send us.
	chunkSize          = 0x4000     // 16KiB
	bep20              = "-GT0000-" // Peer ID client identifier prefix
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

func lastChunkSpec(pieceLength pp.Integer) (cs chunkSpec) {
	cs.Begin = (pieceLength - 1) / chunkSize * chunkSize
	cs.Length = pieceLength - cs.Begin
	return
}

type chunkSpec struct {
	Begin, Length pp.Integer
}

type request struct {
	Index pp.Integer
	chunkSpec
}

func newRequest(index, begin, length pp.Integer) request {
	return request{index, chunkSpec{begin, length}}
}

var (
	// Requested data not yet available.
	errDataNotReady = errors.New("data not ready")
)

// The size in bytes of a metadata extension piece.
func metadataPieceSize(totalSize int, piece int) int {
	ret := totalSize - piece*(1<<14)
	if ret > 1<<14 {
		ret = 1 << 14
	}
	return ret
}

type superer interface {
	Super() interface{}
}

// Returns ok if there's a parent, and it's not nil.
func super(child interface{}) (parent interface{}, ok bool) {
	s, ok := child.(superer)
	if !ok {
		return
	}
	parent = s.Super()
	ok = parent != nil
	return
}
