package torrent

import (
	"io"

	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/storage"
)

func (t *Torrent) storageReader() storageReader {
	if t.storage.NewReader == nil {
		return &storagePieceReader{t: t}
	}
	return torrentStorageImplReader{
		implReader: t.storage.NewReader(),
		t:          t,
	}
}

// This wraps per-piece storage as a whole-torrent storageReader.
type storagePieceReader struct {
	t       *Torrent
	pr      storage.PieceReader
	prIndex int
}

func (me *storagePieceReader) Close() (err error) {
	if me.pr != nil {
		err = me.pr.Close()
	}
	return
}

func (me *storagePieceReader) getReaderAt(p *Piece) (err error) {
	if me.pr != nil {
		if me.prIndex == p.index {
			return
		}
		panicif.Err(me.pr.Close())
		me.pr = nil
	}
	ps := p.Storage()
	me.prIndex = p.index
	me.pr, err = ps.NewReader()
	return
}

func (me *storagePieceReader) ReadAt(b []byte, off int64) (n int, err error) {
	for len(b) != 0 {
		p := me.t.pieceForOffset(off)
		p.waitNoPendingWrites()
		var n1 int
		err = me.getReaderAt(p)
		if err != nil {
			return
		}
		n1, err = me.pr.ReadAt(b, off-p.Info().Offset())
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

// This wraps a storage impl provided TorrentReader as a storageReader.
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
