package storage

import (
	"fmt"
	"io"
	"iter"
	"os"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

type filePieceImpl struct {
	t *fileTorrentImpl
	p metainfo.Piece
	io.WriterAt
	io.ReaderAt
}

var _ PieceImpl = (*filePieceImpl)(nil)

func (me *filePieceImpl) pieceKey() metainfo.PieceKey {
	return metainfo.PieceKey{me.t.infoHash, me.p.Index()}
}

func (fs *filePieceImpl) extent() segments.Extent {
	return segments.Extent{
		Start:  fs.p.Offset(),
		Length: fs.p.Length(),
	}
}

func (fs *filePieceImpl) pieceFiles() iter.Seq2[int, file] {
	return func(yield func(int, file) bool) {
		for fileIndex := range fs.t.segmentLocater.LocateIter(fs.extent()) {
			if !yield(fileIndex, fs.t.files[fileIndex]) {
				return
			}
		}
	}
}

func (fs *filePieceImpl) Completion() Completion {
	c := fs.t.getCompletion(fs.p.Index())
	if !c.Ok {
		return c
	}
	verified := true
	if c.Complete {
		// If it's allegedly complete, check that its constituent files have the necessary length.
		if !fs.t.segmentLocater.Locate(
			fs.extent(),
			func(i int, extent segments.Extent) bool {
				file := fs.t.files[i]
				s, err := os.Stat(file.safeOsPath)
				if err != nil || s.Size() < extent.Start+extent.Length {
					verified = false
					return false
				}
				return true
			}) {
			panic("files do not cover piece extent")
		}
	}

	if !verified {
		// The completion was wrong, fix it. TODO: Should we use MarkNotComplete?
		c.Complete = false
		fs.t.completion.Set(fs.pieceKey(), false)
	}

	return c
}

func (fs *filePieceImpl) MarkComplete() (err error) {
	err = fs.t.completion.Set(fs.pieceKey(), true)
	if err != nil {
		return
	}
nextFile:
	for i, f := range fs.pieceFiles() {
		for p := f.beginPieceIndex; p < f.endPieceIndex; p++ {
			_ = i
			//fmt.Printf("%v %#v %v\n", i, f, p)
			cmpl := fs.t.getCompletion(p)
			if !cmpl.Ok || !cmpl.Complete {
				continue nextFile
			}
		}
		err = fs.t.promotePartFile(f)
		if err != nil {
			err = fmt.Errorf("error promoting part file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return
}

func (fs *filePieceImpl) MarkNotComplete() error {
	return fs.t.completion.Set(fs.pieceKey(), false)
}
