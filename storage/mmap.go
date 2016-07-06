package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/anacrolix/missinggo"
	"github.com/edsrzf/mmap-go"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mmap_span"
)

type mmapStorage struct {
	baseDir    string
	completion pieceCompletion
}

func NewMMap(baseDir string) Client {
	return &mmapStorage{
		baseDir:    baseDir,
		completion: pieceCompletionForDir(baseDir),
	}
}

func (s *mmapStorage) OpenTorrent(info *metainfo.InfoEx) (t Torrent, err error) {
	span, err := mMapTorrent(&info.Info, s.baseDir)
	t = &mmapTorrentStorage{
		span: span,
		pc:   s.completion,
	}
	return
}

type mmapTorrentStorage struct {
	span mmap_span.MMapSpan
	pc   pieceCompletion
}

func (ts *mmapTorrentStorage) Piece(p metainfo.Piece) Piece {
	return mmapStoragePiece{
		pc:       ts.pc,
		p:        p,
		ReaderAt: io.NewSectionReader(ts.span, p.Offset(), p.Length()),
		WriterAt: missinggo.NewSectionWriter(ts.span, p.Offset(), p.Length()),
	}
}

func (ts *mmapTorrentStorage) Close() error {
	ts.pc.Close()
	return ts.span.Close()
}

type mmapStoragePiece struct {
	pc pieceCompletion
	p  metainfo.Piece
	io.ReaderAt
	io.WriterAt
}

func (sp mmapStoragePiece) GetIsComplete() bool {
	return sp.pc.Get(sp.p)
}

func (sp mmapStoragePiece) MarkComplete() error {
	sp.pc.Set(sp.p, true)
	return nil
}

func mMapTorrent(md *metainfo.Info, location string) (mms mmap_span.MMapSpan, err error) {
	defer func() {
		if err != nil {
			mms.Close()
		}
	}()
	for _, miFile := range md.UpvertedFiles() {
		fileName := filepath.Join(append([]string{location, md.Name}, miFile.Path...)...)
		err = os.MkdirAll(filepath.Dir(fileName), 0777)
		if err != nil {
			err = fmt.Errorf("error creating data directory %q: %s", filepath.Dir(fileName), err)
			return
		}
		var file *os.File
		file, err = os.OpenFile(fileName, os.O_CREATE|os.O_RDWR, 0666)
		if err != nil {
			return
		}
		func() {
			defer file.Close()
			var fi os.FileInfo
			fi, err = file.Stat()
			if err != nil {
				return
			}
			if fi.Size() < miFile.Length {
				err = file.Truncate(miFile.Length)
				if err != nil {
					return
				}
			}
			if miFile.Length == 0 {
				// Can't mmap() regions with length 0.
				return
			}
			var mMap mmap.MMap
			mMap, err = mmap.MapRegion(file,
				int(miFile.Length), // Probably not great on <64 bit systems.
				mmap.RDWR, 0, 0)
			if err != nil {
				err = fmt.Errorf("error mapping file %q, length %d: %s", file.Name(), miFile.Length, err)
				return
			}
			if int64(len(mMap)) != miFile.Length {
				panic("mmap has wrong length")
			}
			mms.Append(mMap)
		}()
		if err != nil {
			return
		}
	}
	return
}
