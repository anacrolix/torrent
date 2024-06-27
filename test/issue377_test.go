package test

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/anacrolix/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

func justOneNetwork(cc *torrent.ClientConfig) {
	cc.DisableTCP = true
	cc.DisableIPv6 = true
}

func TestReceiveChunkStorageFailureSeederFastExtensionDisabled(t *testing.T) {
	testReceiveChunkStorageFailure(t, false)
}

func TestReceiveChunkStorageFailure(t *testing.T) {
	testReceiveChunkStorageFailure(t, true)
}

func testReceiveChunkStorageFailure(t *testing.T, seederFast bool) {
	seederDataDir, metainfo := testutil.GreetingTestTorrent()
	defer os.RemoveAll(seederDataDir)
	seederClientConfig := torrent.TestingConfig(t)
	seederClientConfig.Debug = true
	justOneNetwork(seederClientConfig)
	seederClientStorage := storage.NewMMap(seederDataDir)
	defer seederClientStorage.Close()
	seederClientConfig.DefaultStorage = seederClientStorage
	seederClientConfig.Seed = true
	seederClientConfig.Debug = true
	seederClientConfig.Extensions.SetBit(pp.ExtensionBitFast, seederFast)
	seederClient, err := torrent.NewClient(seederClientConfig)
	require.NoError(t, err)
	defer seederClient.Close()
	defer testutil.ExportStatusWriter(seederClient, "s", t)()
	leecherClientConfig := torrent.TestingConfig(t)
	leecherClientConfig.Debug = true
	// Don't require fast extension, whether the seeder will provide it or not (so we can test mixed
	// cases).
	leecherClientConfig.MinPeerExtensions.SetBit(pp.ExtensionBitFast, false)
	justOneNetwork(leecherClientConfig)
	leecherClient, err := torrent.NewClient(leecherClientConfig)
	require.NoError(t, err)
	defer leecherClient.Close()
	defer testutil.ExportStatusWriter(leecherClient, "l", t)()
	info, err := metainfo.UnmarshalInfo()
	require.NoError(t, err)
	leecherStorage := diskFullStorage{
		pieces: make([]pieceState, info.NumPieces()),
		data:   make([]byte, info.TotalLength()),
	}
	defer leecherStorage.Close()
	leecherTorrent, new, err := leecherClient.AddTorrentSpec(&torrent.TorrentSpec{
		InfoHash: metainfo.HashInfoBytes(),
		Storage:  &leecherStorage,
	})
	leecherStorage.t = leecherTorrent
	require.NoError(t, err)
	assert.True(t, new)
	seederTorrent, err := seederClient.AddTorrent(metainfo)
	require.NoError(t, err)
	// Tell the seeder to find the leecher. Is it guaranteed seeders will always try to do this?
	seederTorrent.AddClientPeer(leecherClient)
	<-leecherTorrent.GotInfo()
	r := leecherTorrent.Files()[0].NewReader()
	defer r.Close()
	// We can't use assertReadAllGreeting here, because the default storage write error handler
	// disables data downloads, which now causes Readers to error when they're blocked.
	if false {
		assertReadAllGreeting(t, leecherTorrent.NewReader())
	} else {
		for func() bool {
			// We don't seem to need to seek, but that's probably just because the storage failure is
			// happening on the first read.
			r.Seek(0, io.SeekStart)
			if err := iotest.TestReader(r, []byte(testutil.GreetingFileContents)); err != nil {
				t.Logf("got error while reading: %v", err)
				return true
			}
			return false
		}() {
		}
	}
	// TODO: Check that PeerConns fastEnabled matches seederFast?
	// select {}
}

type pieceState struct {
	complete bool
}

type diskFullStorage struct {
	pieces                        []pieceState
	t                             *torrent.Torrent
	defaultHandledWriteChunkError bool
	data                          []byte

	mu          sync.Mutex
	diskNotFull bool
}

func (me *diskFullStorage) Piece(p metainfo.Piece) storage.PieceImpl {
	return pieceImpl{
		mip:             p,
		diskFullStorage: me,
	}
}

func (me *diskFullStorage) Close() error {
	return nil
}

func (d *diskFullStorage) OpenTorrent(
	_ context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	return storage.TorrentImpl{Piece: d.Piece, Close: d.Close}, nil
}

type pieceImpl struct {
	mip metainfo.Piece
	*diskFullStorage
}

func (me pieceImpl) state() *pieceState {
	return &me.diskFullStorage.pieces[me.mip.Index()]
}

func (me pieceImpl) ReadAt(p []byte, off int64) (n int, err error) {
	off += me.mip.Offset()
	return copy(p, me.data[off:]), nil
}

func (me pieceImpl) WriteAt(p []byte, off int64) (int, error) {
	off += me.mip.Offset()
	if !me.defaultHandledWriteChunkError {
		go func() {
			me.t.SetOnWriteChunkError(func(err error) {
				log.Printf("got write chunk error to custom handler: %v", err)
				me.mu.Lock()
				me.diskNotFull = true
				me.mu.Unlock()
				me.t.AllowDataDownload()
			})
			me.t.AllowDataDownload()
		}()
		me.defaultHandledWriteChunkError = true
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.diskNotFull {
		return copy(me.data[off:], p), nil
	}
	return copy(me.data[off:], p[:1]), errors.New("disk full")
}

func (me pieceImpl) MarkComplete() error {
	me.state().complete = true
	return nil
}

func (me pieceImpl) MarkNotComplete() error {
	panic("implement me")
}

func (me pieceImpl) Completion() storage.Completion {
	return storage.Completion{
		Complete: me.state().complete,
		Ok:       true,
	}
}
