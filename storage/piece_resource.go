package storage

import (
	"io"
	"path"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/uniform"

	"github.com/anacrolix/torrent/metainfo"
)

type piecePerResource struct {
	p uniform.Provider
}

func NewPiecePerResource(p uniform.Provider) Client {
	return &piecePerResource{
		p: p,
	}
}

func (s *piecePerResource) OpenTorrent(info *metainfo.InfoEx) (Torrent, error) {
	return s, nil
}

func (s *piecePerResource) Close() error {
	return nil
}

func (s *piecePerResource) Piece(p metainfo.Piece) Piece {
	completed, err := s.p.NewResource(path.Join("completed", p.Hash().HexString()))
	if err != nil {
		panic(err)
	}
	incomplete, err := s.p.NewResource(path.Join("incomplete", p.Hash().HexString()))
	if err != nil {
		panic(err)
	}
	return piecePerResourcePiece{
		p: p,
		c: completed,
		i: incomplete,
	}
}

type piecePerResourcePiece struct {
	p metainfo.Piece
	c uniform.Resource
	i uniform.Resource
}

func (s piecePerResourcePiece) GetIsComplete() bool {
	fi, err := s.c.Stat()
	return err == nil && fi.Size() == s.p.Length()
}

func (s piecePerResourcePiece) MarkComplete() error {
	return uniform.Move(s.i, s.c)
}

func (s piecePerResourcePiece) ReadAt(b []byte, off int64) (n int, err error) {
	missinggo.LimitLen(&b, s.p.Length()-off)
	n, err = s.c.ReadAt(b, off)
	if err != nil {
		n, err = s.i.ReadAt(b, off)
	}
	off += int64(n)
	if off >= s.p.Length() {
		err = io.EOF
	} else if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return
}

func (s piecePerResourcePiece) WriteAt(b []byte, off int64) (n int, err error) {
	missinggo.LimitLen(&b, s.p.Length()-off)
	return s.i.WriteAt(b, off)
}
