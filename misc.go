package torrent

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"net"
	"time"

	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/internal/netx"
	"github.com/james-lawrence/torrent/metainfo"
)

type chunkSpec struct {
	Begin, Length pp.Integer
}

type request struct {
	Digest   uint64
	Index    pp.Integer
	Reserved time.Time
	Priority int
	chunkSpec
}

func (r request) rDigest(i, b, l uint32) uint64 {
	bs := make([]byte, 12)
	binary.LittleEndian.PutUint32(bs[:4], uint32(i))
	binary.LittleEndian.PutUint32(bs[4:8], uint32(b))
	binary.LittleEndian.PutUint32(bs[8:], uint32(l))
	digest := fnv.New64a()
	digest.Write(bs)
	return digest.Sum64()
}

func (r request) ToMsg(mt pp.MessageType) pp.Message {
	return pp.Message{
		Type:   mt,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	}
}

func newRequest(index, begin, length pp.Integer) request {
	return request{
		Digest:    request{}.rDigest(uint32(index), uint32(begin), uint32(length)),
		Index:     index,
		chunkSpec: chunkSpec{begin, length},
	}
}

func newRequest2(index, begin, length pp.Integer, prio int) request {
	return request{
		Digest:    request{}.rDigest(uint32(index), uint32(begin), uint32(length)),
		Index:     index,
		Priority:  prio,
		chunkSpec: chunkSpec{begin, length},
		Reserved:  time.Now(),
	}
}

func newRequestFromMessage(msg *pp.Message) request {
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
func metadataPieceSize(totalSize int, piece int) int {
	ret := totalSize - piece*(1<<14)
	if ret > 1<<14 {
		ret = 1 << 14
	}
	return ret
}

// Return the request that would include the given offset into the torrent data.
func torrentOffsetRequest(torrentLength, pieceSize, chunkSize, offset int64) (
	r request, ok bool) {
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

	r = newRequest(r.Index, r.Begin, r.Length)
	ok = true
	return
}

func validateInfo(info *metainfo.Info) error {
	if len(info.Pieces)%20 != 0 {
		return errors.New("pieces has invalid length")
	}
	if info.PieceLength == 0 {
		if info.TotalLength() != 0 {
			return errors.New("zero piece length")
		}
	} else {
		if int((info.TotalLength()+info.PieceLength-1)/info.PieceLength) != info.NumPieces() {
			return errors.New("piece count and file lengths are at odds")
		}
	}
	return nil
}

func chunkIndexSpec(index pp.Integer, pieceLength, chunkSize pp.Integer) chunkSpec {
	ret := chunkSpec{pp.Integer(index) * chunkSize, chunkSize}
	if ret.Begin+ret.Length > pieceLength {
		ret.Length = pieceLength - ret.Begin
	}
	return ret
}

func connIsIpv6(nc interface {
	LocalAddr() net.Addr
}) bool {
	ra := nc.LocalAddr()
	rip := netx.NetIPOrNil(ra)
	return rip.To4() == nil && rip.To16() != nil
}
