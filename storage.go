package torrent

import (
	"io"

	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/storage"
)

func (t *Torrent) storageReader() storageReader {
	if t.storage.NewReader == nil {
		return storagePieceReader{t: t}
	}
	return torrentStorageImplReader{
		implReader: t.storage.NewReader(),
		t:          t,
	}
}

type storagePieceReader struct {
	t *Torrent
}

func (me storagePieceReader) Close() error {
	return nil
}

func (me storagePieceReader) ReadAt(b []byte, off int64) (n int, err error) {
	for len(b) != 0 {
		p := me.t.pieceForOffset(off)
		p.waitNoPendingWrites()
		var n1 int
		n1, err = p.Storage().ReadAt(b, off-p.Info().Offset())
		if n1 == 0 {
			panicif.Nil(err)
			break
		}
		off += int64(n1)
		n += n1
		b = b[n1:]
	}
	return
}

type storageReader interface {
	io.ReaderAt
	io.Closer
}

type torrentStorageImplReader struct {
	implReader storage.TorrentReader
	t          *Torrent
}

func (me torrentStorageImplReader) ReadAt(p []byte, off int64) (n int, err error) {
	// TODO: Should waitNoPendingWrites take a region?
	me.t.pieceForOffset(off).waitNoPendingWrites()
	return me.implReader.ReadAt(p, off)
}

func (me torrentStorageImplReader) Close() error {
	return me.implReader.Close()
}
