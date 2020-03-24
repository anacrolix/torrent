package storage

import (
	"encoding/binary"
	"path/filepath"
	"time"

	"github.com/anacrolix/missinggo/expect"
	"go.etcd.io/bbolt"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	// Chosen to match the usual chunk size in a torrent client. This way,
	// most chunk writes are to exactly one full item in bbolt DB.
	chunkSize = 1 << 14
)

type boltDBClient struct {
	db *bbolt.DB
}

type boltDBTorrent struct {
	cl *boltDBClient
	ih metainfo.Hash
}

func NewBoltDB(filePath string) ClientImplCloser {
	db, err := bbolt.Open(filepath.Join(filePath, "bolt.db"), 0600, &bbolt.Options{
		Timeout: time.Second,
	})
	expect.Nil(err)
	db.NoSync = true
	return &boltDBClient{db}
}

func (me *boltDBClient) Close() error {
	return me.db.Close()
}

func (me *boltDBClient) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	return &boltDBTorrent{me, infoHash}, nil
}

func (me *boltDBTorrent) Piece(p metainfo.Piece) PieceImpl {
	ret := &boltDBPiece{
		p:  p,
		db: me.cl.db,
		ih: me.ih,
	}
	copy(ret.key[:], me.ih[:])
	binary.BigEndian.PutUint32(ret.key[20:], uint32(p.Index()))
	return ret
}

func (boltDBTorrent) Close() error { return nil }
