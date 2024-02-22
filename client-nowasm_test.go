//go:build !wasm
// +build !wasm

package torrent

import (
	"os"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

func TestBoltPieceCompletionClosedWhenClientClosed(t *testing.T) {
	c := qt.New(t)
	cfg := TestingConfig(t)
	pc, err := storage.NewBoltPieceCompletion(cfg.DataDir)
	require.NoError(t, err)
	ci := storage.NewFileWithCompletion(cfg.DataDir, pc)
	defer ci.Close()
	cfg.DefaultStorage = ci
	cl, err := NewClient(cfg)
	c.Assert(err, qt.IsNil, qt.Commentf("%#v", err))
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
	c := qt.New(t)
	c.Assert(err, qt.IsNil)
	defer logErr(comp.Close, "closing bolt piece completion")
	mmapStorage := storage.NewMMapWithCompletion(dir, comp)
	defer logErr(mmapStorage.Close, "closing mmap storage")
	cfg.DefaultStorage = mmapStorage
	cl, err := NewClient(cfg)
	c.Assert(err, qt.IsNil)
	defer cl.Close()
	tor, new, err := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	c.Assert(err, qt.IsNil)
	c.Assert(new, qt.IsTrue)
	c.Assert(cl.WaitAll(), qt.IsTrue)
	tor.Drop()
	_, new, err = cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	c.Assert(err, qt.IsNil)
	c.Assert(new, qt.IsTrue)
	c.Assert(cl.WaitAll(), qt.IsTrue)
}
