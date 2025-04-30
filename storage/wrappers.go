package storage

import (
	"context"
	"io"
	"os"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2"

	"github.com/anacrolix/torrent/metainfo"
)

type Client struct {
	ci ClientImpl
}

func NewClient(cl ClientImpl) *Client {
	return &Client{cl}
}

func (cl Client) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (*Torrent, error) {
	t, err := cl.ci.OpenTorrent(ctx, info, infoHash)
	if err != nil {
		return nil, err
	}
	return &Torrent{t}, nil
}

type Torrent struct {
	TorrentImpl
}

// Deprecated. Use PieceWithHash, as this doesn't work with pure v2 torrents.
func (t Torrent) Piece(p metainfo.Piece) Piece {
	return t.PieceWithHash(p, g.Some(p.V1Hash().Unwrap().Bytes()))
}

func (t Torrent) PieceWithHash(p metainfo.Piece, pieceHash g.Option[[]byte]) Piece {
	var pieceImpl PieceImpl
	if t.TorrentImpl.PieceWithHash != nil {
		pieceImpl = t.TorrentImpl.PieceWithHash(p, pieceHash)
	} else {
		pieceImpl = t.TorrentImpl.Piece(p)
	}
	return Piece{pieceImpl, p}
}

type Piece struct {
	PieceImpl
	mip metainfo.Piece
}

var _ io.WriterTo = Piece{}

// Why do we have this wrapper? Well PieceImpl doesn't implement io.Reader, so we can't let io.Copy
// and friends check for io.WriterTo and fallback for us since they expect an io.Reader.
func (p Piece) WriteTo(w io.Writer) (int64, error) {
	if i, ok := p.PieceImpl.(io.WriterTo); ok {
		return i.WriteTo(w)
	}
	n := p.mip.Length()
	r := io.NewSectionReader(p, 0, n)
	return io.CopyN(w, r, n)
}

func (p Piece) WriteAt(b []byte, off int64) (n int, err error) {
	// Callers should not be writing to completed pieces, but it's too
	// expensive to be checking this on every single write using uncached
	// completions.

	// c := p.Completion()
	// if c.Ok && c.Complete {
	// 	err = errors.New("piece already completed")
	// 	return
	// }
	if off+int64(len(b)) > p.mip.Length() {
		panic("write overflows piece")
	}
	b = missinggo.LimitLen(b, p.mip.Length()-off)
	return p.PieceImpl.WriteAt(b, off)
}

// If you're calling this you're probably doing something very inefficient. Consider WriteTo which
// handles data spread across multiple objects in storage.
func (p Piece) ReadAt(b []byte, off int64) (n int, err error) {
	if off < 0 {
		err = os.ErrInvalid
		return
	}
	if off >= p.mip.Length() {
		err = io.EOF
		return
	}
	b = missinggo.LimitLen(b, p.mip.Length()-off)
	if len(b) == 0 {
		return
	}
	n, err = p.PieceImpl.ReadAt(b, off)
	if n > len(b) {
		panic(n)
	}
	if n == 0 && err == nil {
		panic("io.Copy will get stuck")
	}
	off += int64(n)

	// Doing this here may be inaccurate. There's legitimate reasons we may fail to read while the
	// data is still there, such as too many open files. There should probably be a specific error
	// to return if the data has been lost.
	if off < p.mip.Length() {
		if err == io.EOF {
			p.MarkNotComplete()
		}
	}

	return
}
