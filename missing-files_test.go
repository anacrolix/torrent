package torrent

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Pieces whose data isn't in storage at all are known to be incomplete without hashing them, so
// they can be requested at once instead of waiting for a check of the data that is there.
func TestPiecesOfMissingDataAreNotQueuedForHash(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	data, err := os.ReadFile("testdata/v2-multi-piece/data.bin")
	qt.Assert(t, qt.IsNil(err))
	info, err := mi.UnmarshalInfo()
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(info.NumPieces() >= 3), qt.Commentf("pieces: %v", info.NumPieces()))

	for _, tc := range []struct {
		name string
		// How much of the data is in storage, nil for no file.
		have []byte
		// The pieces still to be checked: those wholly in storage.
		queued int
	}{
		{"no file", nil, 0},
		{"first piece", data[:info.PieceLength], 1},
		{"all but a byte", data[:len(data)-1], info.NumPieces() - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.have != nil {
				path := filepath.Join(dir, "v2multi", "data.bin")
				qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
				qt.Assert(t, qt.IsNil(os.WriteFile(path, tc.have, 0o644)))
			}
			cfg := TestingConfig(t)
			cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir})
			// Keep the queue as adding the torrent left it.
			cfg.PieceHashersPerTorrent = 0
			cl, err := NewClient(cfg)
			qt.Assert(t, qt.IsNil(err))
			defer cl.Close()
			tt, err := cl.AddTorrent(mi)
			qt.Assert(t, qt.IsNil(err))
			<-tt.GotInfo()

			cl.lock()
			defer cl.unlock()
			queued := int(tt.piecesQueuedForHash.GetCardinality()) + tt.activePieceHashes
			qt.Check(t, qt.Equals(queued, tc.queued))
			for i := tc.queued; i < tt.NumPieces(); i++ {
				p := tt.piece(i)
				qt.Check(t, qt.IsTrue(p.state().storageCompletionOk), qt.Commentf("piece %v", i))
				qt.Check(t, qt.IsFalse(p.ignoreForRequests()), qt.Commentf("piece %v", i))
			}
		})
	}
}
