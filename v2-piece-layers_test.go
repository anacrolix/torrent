package torrent

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Adding a v2 torrent applies its piece layers while the info is being set, before the torrent's
// piece request order exists. With the data already on disk that marks pieces complete, which
// must not trip the pending pieces check.
func TestAddV2TorrentWithPieceLayersAndDataOnDisk(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Not(qt.HasLen(mi.PieceLayers, 0)))

	dir := t.TempDir()
	data, err := os.ReadFile("testdata/v2-multi-piece/data.bin")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "v2multi"), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "v2multi", "data.bin"), data, 0o644)))

	cfg := TestingConfig(t)
	cfg.DataDir = dir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	qt.Assert(t, qt.IsNil(tt.VerifyData()))
	qt.Check(t, qt.Equals(tt.BytesCompleted(), tt.Length()))
}
