//go:build !wasm
// +build !wasm

package torrent

import (
	"os"
	"testing"

	qt "github.com/go-quicktest/qt"
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
	qt.Assert(t, qt.IsNil(err), qt.Commentf("%#v", err))
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
	logErr := func(f func() error, msg string) {
		err := f()
		t.Logf("%s: %v", msg, err)
		if err != nil {
			t.Fail()
		}
	}
	cfg := TestingConfig(t)
	cfg.Seed = false
	cfg.Debug = true
	cfg.DataDir = dir
	comp, err := storage.NewBoltPieceCompletion(dir)
	qt.Assert(t, qt.IsNil(err))
	defer logErr(comp.Close, "closing bolt piece completion")
	mmapStorage := storage.NewMMapWithCompletion(dir, comp)
	defer mmapStorage.Close()
	cfg.DefaultStorage = mmapStorage
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tor, new, err := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(new))
	qt.Assert(t, qt.IsTrue(cl.WaitAll()))
	tor.Drop()
	_, new, err = cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(new))
	qt.Assert(t, qt.IsTrue(cl.WaitAll()))
}
