//go:build cgo
// +build cgo

package sqliteStorage

import (
	"context"
	"encoding/hex"
	"io"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
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
		cache: cache,
	}, nil
}

// Creates a storage.ClientImpl from a provided squirrel.Cache. The caller is responsible for
// closing the squirrel.Cache.
func NewWrappingClient(cache *squirrel.Cache) storage.ClientImpl {
	return &client{
		cache: cache,
	}
}

type client struct {
	cache           *squirrel.Cache
	capacityMu      sync.Mutex
	capacityFetched time.Time
	capacityCap     int64
	capacityCapped  bool
}

func (c *client) Close() error {
	return c.cache.Close()
}

func (c *client) capacity() (cap int64, capped bool) {
	c.capacityMu.Lock()
	defer c.capacityMu.Unlock()
	if !c.capacityFetched.IsZero() && time.Since(c.capacityFetched) < time.Second {
		cap, capped = c.capacityCap, c.capacityCapped
		return
	}
	c.capacityCap, c.capacityCapped = c.cache.GetCapacity()
	// Should this go before or after the capacityCap and capacityCapped assignments?
	c.capacityFetched = time.Now()
	cap, capped = c.capacityCap, c.capacityCapped
	return
}

func (c *client) OpenTorrent(
	context.Context,
	*metainfo.Info,
	metainfo.Hash,
) (storage.TorrentImpl, error) {
	t := torrent{c.cache}
	capFunc := c.capacity
	return storage.TorrentImpl{PieceWithHash: t.Piece, Close: t.Close, Capacity: &capFunc}, nil
}

type torrent struct {
	c *squirrel.Cache
}

func (t torrent) Piece(p metainfo.Piece, pieceHash g.Option[[]byte]) storage.PieceImpl {
	ret := piece{
		sb: t.c.OpenWithLength(hex.EncodeToString(pieceHash.Unwrap()), p.Length()),
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
	err := p.sb.GetTag("verified", func(stmt squirrel.SqliteStmt) {
		ret.Complete = stmt.ColumnInt(0) != 0
	})
	ret.Ok = err == nil
	ret.Err = err
	return
}
