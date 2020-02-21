package test

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestReceiveChunkStorageFailure(t *testing.T) {
	seederDataDir, metainfo := testutil.GreetingTestTorrent()
	defer os.RemoveAll(seederDataDir)
	seederClientConfig := torrent.TestingConfig()
	seederClientConfig.Debug = true
	seederClientStorage := storage.NewMMap(seederDataDir)
	defer seederClientStorage.Close()
	seederClientConfig.DefaultStorage = seederClientStorage
	seederClientConfig.Seed = true
	seederClientConfig.Debug = true
	seederClient, err := torrent.NewClient(seederClientConfig)
	require.NoError(t, err)
	defer testutil.ExportStatusWriter(seederClient, "s")()
	leecherClientConfig := torrent.TestingConfig()
	leecherClientConfig.Debug = true
	leecherClient, err := torrent.NewClient(leecherClientConfig)
	require.NoError(t, err)
	defer testutil.ExportStatusWriter(leecherClient, "l")()
	leecherTorrent, new, err := leecherClient.AddTorrentSpec(&torrent.TorrentSpec{
		InfoHash: metainfo.HashInfoBytes(),
		Storage:  diskFullStorage{},
	})
	require.NoError(t, err)
	assert.True(t, new)
	seederTorrent, err := seederClient.AddTorrent(metainfo)
	require.NoError(t, err)
	// Tell the seeder to find the leecher. Is it guaranteed seeders will always try to do this?
	seederTorrent.AddClientPeer(leecherClient)
	//leecherTorrent.AddClientPeer(seederClient)
	<-leecherTorrent.GotInfo()
	assertReadAllGreeting(t, leecherTorrent.NewReader())
}

type diskFullStorage struct{}

func (me diskFullStorage) ReadAt(p []byte, off int64) (n int, err error) {
	panic("implement me")
}

func (me diskFullStorage) WriteAt(p []byte, off int64) (n int, err error) {
	return 1, errors.New("disk full")
}

func (me diskFullStorage) MarkComplete() error {
	panic("implement me")
}

func (me diskFullStorage) MarkNotComplete() error {
	panic("implement me")
}

func (me diskFullStorage) Completion() storage.Completion {
	return storage.Completion{
		Complete: false,
		Ok:       true,
	}
}

func (me diskFullStorage) Piece(metainfo.Piece) storage.PieceImpl {
	return me
}

func (me diskFullStorage) Close() error {
	panic("implement me")
}

func (d diskFullStorage) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	return d, nil
}
