package torrent

import (
	"errors"
	"fmt"
	"math"
	"net"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
	"github.com/anacrolix/torrent/types/infohash"
)

type (
	// TODO: Make this private. Use types.Request in the (one?) place it's exposed here.
	Request       = types.Request
	ChunkSpec     = types.ChunkSpec
	PiecePriority = types.PiecePriority
)

const (
	PiecePriorityNormal    = types.PiecePriorityNormal
	PiecePriorityNone      = types.PiecePriorityNone
	PiecePriorityNow       = types.PiecePriorityNow
	PiecePriorityReadahead = types.PiecePriorityReadahead
	PiecePriorityNext      = types.PiecePriorityNext
	PiecePriorityHigh      = types.PiecePriorityHigh
)

func newRequest(index, begin, length pp.Integer) Request {
	return Request{Index: index, ChunkSpec: ChunkSpec{Begin: begin, Length: length}}
}

func newRequestFromMessage(msg *pp.Message) Request {
	switch msg.Type {
	case pp.Request, pp.Cancel, pp.Reject:
		return newRequest(msg.Index, msg.Begin, msg.Length)
	case pp.Piece:
		return newRequest(msg.Index, msg.Begin, pp.Integer(len(msg.Piece)))
	default:
		panic(msg.Type)
	}
}

// The size in bytes of a metadata extension piece.
func metadataPieceSize(totalSize, piece int) int {
	ret := totalSize - piece*(1<<14)
	if ret > 1<<14 {
		ret = 1 << 14
	}
	return ret
}

// Return the request that would include the given offset into the torrent data.
func torrentOffsetRequest(
	torrentLength, pieceSize, chunkSize, offset int64,
) (
	r Request, ok bool,
) {
	if offset < 0 || offset >= torrentLength {
		return
	}
	r.Index = pp.Integer(offset / pieceSize)
	r.Begin = pp.Integer(offset % pieceSize / chunkSize * chunkSize)
	r.Length = pp.Integer(chunkSize)
	pieceLeft := pp.Integer(pieceSize - int64(r.Begin))
	if r.Length > pieceLeft {
		r.Length = pieceLeft
	}
	torrentLeft := torrentLength - int64(r.Index)*pieceSize - int64(r.Begin)
	if int64(r.Length) > torrentLeft {
		r.Length = pp.Integer(torrentLeft)
	}
	ok = true
	return
}

func torrentRequestOffset(torrentLength, pieceSize int64, r Request) (off int64) {
	off = int64(r.Index)*pieceSize + int64(r.Begin)
	if off < 0 || off >= torrentLength {
		panic("invalid Request")
	}
	return
}

// maxInfoPieces caps the number of pieces a v2 info may imply. A v2 file tree declares file
// lengths, not a hash per piece, so without a cap a ~150-byte info can claim a petabyte-scale file
// and make setInfo allocate for billions of pieces. v1 needs no cap: every piece costs a 20-byte
// hash in the info itself. At 1<<24 the backing []Piece stays under 256 MiB (16 bytes each) while
// still allowing, for example, a 4 TiB torrent at 256 KiB pieces or 256 TiB at 16 MiB pieces, far
// beyond any real torrent. It is a tunable policy limit, not a protocol one.
const maxInfoPieces = 1 << 24

// maxPieceLength caps the piece length so that offsets within a piece fit the 32-bit peer protocol
// integers used for chunk arithmetic. Real torrents use far smaller pieces.
const maxPieceLength = 1 << 30

func validateInfo(info *metainfo.Info) error {
	// A negative piece length later reaches Piece.V1Length, which panics.
	if info.PieceLength < 0 {
		return errors.New("negative piece length")
	}
	// Offsets within a piece are 32-bit on the wire and in chunk arithmetic.
	if info.PieceLength > maxPieceLength {
		return fmt.Errorf("piece length %v exceeds %v", info.PieceLength, maxPieceLength)
	}
	// v1 file lengths are used for storage offsets and the piece count check below, so they must be
	// non-negative and their sum must not overflow.
	var v1Total int64
	for fi := range info.UpvertedV1Files() {
		if fi.Length < 0 {
			return fmt.Errorf("negative file length %v", fi.Length)
		}
		if fi.Length > math.MaxInt64-v1Total {
			return errors.New("total file length overflows")
		}
		v1Total += fi.Length
	}
	if info.HasV2() {
		if err := info.FileTree.Validate(); err != nil {
			return fmt.Errorf("bad v2 file tree: %w", err)
		}
		// BEP 52 requires a power-of-two piece length, and the v2 merkle hashing assumes it. This
		// also rejects a zero piece length before anything divides by it.
		if info.PieceLength < 1<<14 || info.PieceLength&(info.PieceLength-1) != 0 {
			return fmt.Errorf("v2 piece length %v is not a power of two of at least 16 KiB", info.PieceLength)
		}
		// Count pieces in int64 with an early bound. Info.NumPieces does the same ceiling division
		// but returns int (which overflows on 32-bit for a huge declared length) and applies no
		// cap, so counting it here is both safer and lets us stop as soon as the bound is passed.
		var numPieces int64
		var err error
		info.FileTree.Walk(nil, func(path []string, ft *metainfo.FileTree) {
			if err != nil || ft.IsDir() {
				return
			}
			// File lengths are non-negative (checked by FileTree.Validate above).
			numPieces += ft.File.Length / info.PieceLength
			if ft.File.Length%info.PieceLength != 0 {
				numPieces++
			}
			if numPieces > maxInfoPieces {
				err = fmt.Errorf("file tree implies more than %v pieces", maxInfoPieces)
			}
		})
		if err != nil {
			return err
		}
		// A hybrid info carries both a v1 hash list and a v2 file tree. If their piece counts
		// disagree, later code indexes the v1 hashes by the v2 count and slices out of range.
		if info.HasV1() {
			if int64(len(info.Pieces)/20) != numPieces {
				return fmt.Errorf("hybrid info has %v v1 piece hashes for %v pieces", len(info.Pieces)/20, numPieces)
			}
			// The v1 file lengths must span the same pieces as the v2 file tree. Otherwise the last
			// piece's v1 length (metainfo.Piece.V1Length) comes out zero or negative and panics the
			// piece hasher.
			if (v1Total+info.PieceLength-1)/info.PieceLength != numPieces {
				return fmt.Errorf("hybrid info v1 file lengths span %v pieces, not %v",
					(v1Total+info.PieceLength-1)/info.PieceLength, numPieces)
			}
		}
	}
	if len(info.Pieces)%20 != 0 {
		return errors.New("pieces has invalid length")
	}
	if info.PieceLength == 0 {
		if info.TotalLength() != 0 {
			return errors.New("zero piece length")
		}
	} else if !info.HasV2() {
		// TotalLength returns different values for V1 and V2 depending on whether v1 pad files are
		// counted. Split the interface into several methods?
		if int((info.TotalLength()+info.PieceLength-1)/info.PieceLength) != info.NumPieces() {
			return errors.New("piece count and file lengths are at odds")
		}
	}
	return nil
}

func chunkIndexSpec(index, pieceLength, chunkSize pp.Integer) ChunkSpec {
	begin := index * chunkSize
	panicif.GreaterThanOrEqual(begin, pieceLength)
	return ChunkSpec{
		Begin:  begin,
		Length: min(chunkSize, pieceLength-begin),
	}
}

func comparePeerTrust(l, r *Peer) int {
	return l.trust().Cmp(r.trust())
}

func connIsIpv6(nc interface {
	LocalAddr() net.Addr
},
) bool {
	ra := nc.LocalAddr()
	rip := addrIpOrNil(ra)
	return rip.To4() == nil && rip.To16() != nil
}

var unlimited = rate.NewLimiter(rate.Inf, 0)

type (
	pieceIndex = int
	// Deprecated: Use infohash.T directly to avoid unnecessary imports.
	InfoHash = infohash.T
	IpPort   = missinggo.IpPort
)

func boolSliceToBitmap(slice []bool) (rb roaring.Bitmap) {
	for i, b := range slice {
		if b {
			rb.AddInt(i)
		}
	}
	return
}
