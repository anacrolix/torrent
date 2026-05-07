package storage

import (
	"log/slog"
	"testing"

	"github.com/go-quicktest/qt"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
)

type trackingFileIo struct {
	flushed []string
	renamed bool
}

func (me *trackingFileIo) openForRead(string) (fileReader, error) {
	panic("unexpected openForRead")
}

func (me *trackingFileIo) openForSharedRead(string) (sharableReader, error) {
	panic("unexpected openForSharedRead")
}

func (me *trackingFileIo) openForWrite(string, int64) (fileWriter, error) {
	panic("unexpected openForWrite")
}

func (me *trackingFileIo) flush(name string, _, _ int64) error {
	me.flushed = append(me.flushed, name)
	return nil
}

func (me *trackingFileIo) rename(string, string) error {
	me.renamed = true
	return nil
}

func (me *trackingFileIo) Close() error {
	return nil
}

func TestPromotePartFileSkipsPartPathWhenDisabled(t *testing.T) {
	ioImpl := &trackingFileIo{}
	torrent := &fileTorrentImpl{
		info: &metainfo.Info{
			PieceLength: 1,
		},
		files: []fileExtra{{
			safeOsPath: "a.bin",
		}},
		metainfoFileInfos: []metainfo.FileInfo{{
			Path:   []string{"a.bin"},
			Length: 1,
		}},
		io: ioImpl,
		client: &fileClientImpl{
			opts: NewFileClientOpts{
				UsePartFiles: g.Option[bool]{Ok: true, Value: false},
				Logger:       slog.Default(),
			},
		},
	}
	pieceImpl := &filePieceImpl{t: torrent}

	err := pieceImpl.promotePartFile(torrent.file(0))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.HasLen(ioImpl.flushed, 0))
	qt.Assert(t, qt.IsFalse(ioImpl.renamed))
}
