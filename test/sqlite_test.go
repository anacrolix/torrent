// This infernal language makes me copy conditional compilation expressions around. This test should
// run if sqlite storage is enabled, period.

//go:build cgo
// +build cgo

package test

import (
	"net"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	sqliteStorage "github.com/anacrolix/torrent/storage/sqlite"
)

func TestSqliteStorageClosed(t *testing.T) {
	c := qt.New(t)
	cfg := torrent.TestingConfig(t)
	storage, err := sqliteStorage.NewDirectStorage(sqliteStorage.NewDirectStorageOpts{})
	defer storage.Close()
	cfg.DefaultStorage = storage
	cfg.Debug = true
	c.Assert(err, qt.IsNil)
	cl, err := torrent.NewClient(cfg)
	c.Assert(err, qt.IsNil)
	defer cl.Close()
	l, err := net.Listen("tcp", "localhost:0")
	c.Assert(err, qt.IsNil)
	defer l.Close()
	// We need at least once piece to trigger a call to storage to determine completion state. We
	// need non-zero content length to trigger piece hashing.
	i := metainfo.Info{
		Pieces:      make([]byte, metainfo.HashSize),
		PieceLength: 1,
		Files: []metainfo.FileInfo{
			{Length: 1},
		},
	}
	mi := metainfo.MetaInfo{}
	mi.InfoBytes, err = bencode.Marshal(i)
	c.Assert(err, qt.IsNil)
	s := http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mi.Write(w)
		}),
	}
	defer s.Close()
	go func() {
		err := s.Serve(l)
		if err != http.ErrServerClosed {
			panic(err)
		}
	}()
	// Close storage prematurely.
	storage.Close()
	tor, _, err := cl.AddTorrentSpec(&torrent.TorrentSpec{
		InfoHash: mi.HashInfoBytes(),
		Sources:  []string{"http://" + l.Addr().String()},
	})
	c.Assert(err, qt.IsNil)
	<-tor.GotInfo()
}
