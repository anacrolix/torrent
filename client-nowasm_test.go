//go:build !wasm
// +build !wasm

package torrent

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

func TestBoltPieceCompletionClosedWhenClientClosed(t *testing.T) {
	cfg := TestingConfig(t)
	pc, err := storage.NewBoltPieceCompletion(cfg.DataDir)
	require.NoError(t, err)
	ci := storage.NewFileWithCompletion(cfg.DataDir, pc)
	defer ci.Close()
	cfg.DefaultStorage = ci
	cl, err := NewClient(cfg)
	require.NoError(t, err)
	cl.Close()
	// And again, https://github.com/anacrolix/torrent/issues/158
	cl, err = NewClient(cfg)
	require.NoError(t, err)
	cl.Close()
}

func TestIssue335(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer func() {
		err := os.RemoveAll(dir)
		if err != nil {
			t.Fatalf("removing torrent dummy data dir: %v", err)
		}
	}()
	cfg := TestingConfig(t)
	cfg.Seed = false
	cfg.Debug = true
	cfg.DataDir = dir
	comp, err := storage.NewBoltPieceCompletion(dir)
	require.NoError(t, err)
	defer comp.Close()
	cfg.DefaultStorage = storage.NewMMapWithCompletion(dir, comp)
	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()
	tor, new, err := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	assert.True(t, new)
	require.True(t, cl.WaitAll())
	tor.Drop()
	_, new, err = cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	assert.True(t, new)
	require.True(t, cl.WaitAll())
}
