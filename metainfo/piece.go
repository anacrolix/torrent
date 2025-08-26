package metainfo

import (
	"fmt"
	"iter"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type Piece struct {
	Info *Info // Can we embed the fields here instead, or is it something to do with saving memory?
	i    PieceIndex
}

func (p Piece) String() string {
	return fmt.Sprintf("metainfo.Piece(Info.Name=%q, i=%v)", p.Info.Name, p.i)
}

type PieceIndex = int

func (p Piece) Length() int64 {
	if p.Info.HasV2() {
		var offset int64
		pieceLength := p.Info.PieceLength
		lastFileEnd := int64(0)
		for fi := range p.Info.FileTree.upvertedFiles(pieceLength) {
			// I don't think we need to track offset ourselves.
			panicif.NotEq(offset, fi.TorrentOffset)
			fileStartPiece := int(offset / pieceLength)
			if fileStartPiece > p.i {
				break
			}
			lastFileEnd = offset + fi.Length
			offset = (lastFileEnd + pieceLength - 1) / pieceLength * pieceLength
		}
		ret := min(lastFileEnd-int64(p.i)*pieceLength, pieceLength)
		if ret <= 0 {
			panic(ret)
		}
		return ret
	}
	return p.V1Length()
}

func iterLast[T any](i iter.Seq[T]) (last g.Option[T]) {
	for t := range i {
		last.Set(t)
	}
	return
}

func (p Piece) V1Length() int64 {
	i := p.i
	lastPiece := p.Info.NumPieces() - 1
	switch {
	case 0 <= i && i < lastPiece:
		return p.Info.PieceLength
	case lastPiece >= 0 && i == lastPiece:
		lastFile := iterLast(p.Info.UpvertedV1Files()).Unwrap()
		length := lastFile.TorrentOffset + lastFile.Length - int64(i)*p.Info.PieceLength
		if length <= 0 || length > p.Info.PieceLength {
			panic(length)
		}
		return length
	default:
		panic(i)
	}
}

func (p Piece) Offset() int64 {
	return int64(p.i) * p.Info.PieceLength
}

func (p Piece) V1Hash() (ret g.Option[Hash]) {
	if !p.Info.HasV1() {
		return
	}
	copy(ret.Value[:], p.Info.Pieces[p.i*HashSize:(p.i+1)*HashSize])
	ret.Ok = true
	return
}

func (p Piece) Index() int {
	return p.i
}
