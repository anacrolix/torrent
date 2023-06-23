//go:build cgo
// +build cgo

package sqliteStorage

import (
	"io"

	"crawshaw.io/sqlite"
	"github.com/anacrolix/squirrel"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// A convenience function that creates a connection pool, resource provider, and a pieces storage
// ClientImpl and returns them all with a Close attached.
func NewDirectStorage(opts NewDirectStorageOpts) (_ storage.ClientImplCloser, err error) {
	cache, err := squirrel.NewCache(opts)
	if err != nil {
		return
	}
	return &client{
		cache,
		cache.GetCapacity,
	}, nil
}

func NewWrappingClient(cache *squirrel.Cache) storage.ClientImpl {
	return &client{
		cache,
		cache.GetCapacity,
	}
}

type client struct {
	*squirrel.Cache
	capacity func() (int64, bool)
}

func (c *client) OpenTorrent(*metainfo.Info, metainfo.Hash) (storage.TorrentImpl, error) {
	t := torrent{c.Cache}
	return storage.TorrentImpl{Piece: t.Piece, Close: t.Close, Capacity: &c.capacity}, nil
}

type torrent struct {
	c *squirrel.Cache
}

func (t torrent) Piece(p metainfo.Piece) storage.PieceImpl {
	ret := piece{
		sb: t.c.OpenWithLength(p.Hash().HexString(), p.Length()),
	}
	ret.ReaderAt = &ret.sb
	ret.WriterAt = &ret.sb
	return ret
}

func (t torrent) Close() error {
	return nil
}

type piece struct {
	sb squirrel.Blob
	io.ReaderAt
	io.WriterAt
}

func (p piece) MarkComplete() error {
	return p.sb.SetTag("verified", true)
}

func (p piece) MarkNotComplete() error {
	return p.sb.SetTag("verified", false)
}

func (p piece) Completion() (ret storage.Completion) {
	err := p.sb.GetTag("verified", func(stmt *sqlite.Stmt) {
		ret.Complete = stmt.ColumnInt(0) != 0
	})
	ret.Ok = err == nil
	ret.Err = err
	return
}
