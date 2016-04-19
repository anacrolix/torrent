package storage

import (
	"errors"
	"io"
	"os"
	"path"

	"github.com/anacrolix/missinggo"

	"github.com/anacrolix/torrent/metainfo"
)

type pieceFileStorage struct {
	fs missinggo.FileStore
}

func NewPieceFileStorage(fs missinggo.FileStore) I {
	return &pieceFileStorage{
		fs: fs,
	}
}

type pieceFileTorrentStorage struct {
	s *pieceFileStorage
}

func (s *pieceFileStorage) OpenTorrent(info *metainfo.InfoEx) (Torrent, error) {
	return &pieceFileTorrentStorage{s}, nil
}

func (s *pieceFileTorrentStorage) Close() error {
	return nil
}

func (s *pieceFileTorrentStorage) Piece(p metainfo.Piece) Piece {
	return pieceFileTorrentStoragePiece{s, p, s.s.fs}
}

type pieceFileTorrentStoragePiece struct {
	ts *pieceFileTorrentStorage
	p  metainfo.Piece
	fs missinggo.FileStore
}

func (s pieceFileTorrentStoragePiece) completedPath() string {
	return path.Join("completed", s.p.Hash().HexString())
}

func (s pieceFileTorrentStoragePiece) incompletePath() string {
	return path.Join("incomplete", s.p.Hash().HexString())
}

func (s pieceFileTorrentStoragePiece) GetIsComplete() bool {
	fi, err := s.fs.Stat(s.completedPath())
	return err == nil && fi.Size() == s.p.Length()
}

func (s pieceFileTorrentStoragePiece) MarkComplete() error {
	return s.fs.Rename(s.incompletePath(), s.completedPath())
}

func (s pieceFileTorrentStoragePiece) openFile() (f missinggo.File, err error) {
	f, err = s.fs.OpenFile(s.completedPath(), os.O_RDONLY)
	if err == nil {
		var fi os.FileInfo
		fi, err = f.Stat()
		if err == nil && fi.Size() == s.p.Length() {
			return
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return
	}
	f, err = s.fs.OpenFile(s.incompletePath(), os.O_RDONLY)
	if os.IsNotExist(err) {
		err = io.ErrUnexpectedEOF
	}
	return
}

func (s pieceFileTorrentStoragePiece) ReadAt(b []byte, off int64) (n int, err error) {
	f, err := s.openFile()
	if err != nil {
		return
	}
	defer f.Close()
	missinggo.LimitLen(&b, s.p.Length()-off)
	n, err = f.ReadAt(b, off)
	off += int64(n)
	if off >= s.p.Length() {
		err = io.EOF
	} else if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return
}

func (s pieceFileTorrentStoragePiece) WriteAt(b []byte, off int64) (n int, err error) {
	if s.GetIsComplete() {
		err = errors.New("piece completed")
		return
	}
	f, err := s.fs.OpenFile(s.incompletePath(), os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return
	}
	defer f.Close()
	missinggo.LimitLen(&b, s.p.Length()-off)
	return f.WriteAt(b, off)
}
