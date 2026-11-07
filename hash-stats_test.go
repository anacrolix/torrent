package torrent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// addV2MultiPiece adds testdata/v2-multi-piece with its data in storage, as prepare leaves it, and
// returns the path of the data file too.
func addV2MultiPiece(t *testing.T, prepare func(data []byte) []byte) (*Client, *Torrent, string) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/v2-multi-piece/data.bin")
	qt.Assert(t, qt.IsNil(err))
	if prepare != nil {
		data = prepare(data)
	}
	path := filepath.Join(dir, "v2multi", "data.bin")
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(path), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(path, data, 0o644)))

	cfg := TestingConfig(t)
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	return cl, tt, path
}

// Checks from adding the torrent may run besides VerifyData, so the tests below expect every piece
// to have been hashed at least once.

func TestStatsCountPiecesHashed(t *testing.T) {
	cl, tt, _ := addV2MultiPiece(t, func(data []byte) []byte { data[0]++; return data })
	qt.Assert(t, qt.IsNil(tt.VerifyData()))

	st := tt.Stats()
	good, bad := st.PiecesHashedGood.Int64(), st.PiecesHashedBad.Int64()
	qt.Check(t, qt.IsTrue(bad >= 1), qt.Commentf("bad: %v", bad))
	qt.Check(t, qt.IsTrue(good >= int64(tt.NumPieces()-1)), qt.Commentf("good: %v of %v pieces", good, tt.NumPieces()))
	qt.Check(t, qt.Equals(st.PiecesHashedMissing.Int64()+st.PiecesHashedErrors.Int64(), 0))
	cst := cl.Stats()
	qt.Check(t, qt.Equals(cst.PiecesHashedGood.Int64(), good))
	qt.Check(t, qt.Equals(cst.PiecesHashedBad.Int64(), bad))
}

func TestStatsCountPiecesMissingFromStorage(t *testing.T) {
	// One byte short: the last piece is incomplete, the others are whole.
	cl, tt, _ := addV2MultiPiece(t, func(data []byte) []byte { return data[:len(data)-1] })
	qt.Assert(t, qt.IsNil(tt.VerifyData()))

	st := tt.Stats()
	missing := st.PiecesHashedMissing.Int64()
	qt.Check(t, qt.IsTrue(missing >= 1), qt.Commentf("missing: %v", missing))
	qt.Check(t, qt.Equals(st.PiecesHashedBad.Int64()+st.PiecesHashedErrors.Int64(), 0))
	cst := cl.Stats()
	qt.Check(t, qt.Equals(cst.PiecesHashedMissing.Int64(), missing))
}

func TestStatsCountPiecesThatCouldNotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't take read permission from the mode bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads the file whatever its mode")
	}
	cl, tt, path := addV2MultiPiece(t, nil)
	qt.Assert(t, qt.IsNil(os.Chmod(path, 0)))
	qt.Assert(t, qt.IsNil(tt.VerifyData()))

	st := tt.Stats()
	errs := st.PiecesHashedErrors.Int64()
	qt.Check(t, qt.IsTrue(errs >= int64(tt.NumPieces())), qt.Commentf("errors: %v of %v pieces", errs, tt.NumPieces()))
	cst := cl.Stats()
	qt.Check(t, qt.Equals(cst.PiecesHashedErrors.Int64(), errs))
}

func TestStatsCountPiecesQueuedForHash(t *testing.T) {
	cl, tt, _ := addV2MultiPiece(t, nil)

	// Let the checks from adding the torrent finish, then queue every piece and look under the
	// same lock: the hashers started meanwhile have taken their pieces off the queue.
	deadline := time.Now().Add(10 * time.Second)
	for {
		cl.lock()
		if cl.activePieceHashers == 0 || time.Now().After(deadline) {
			break
		}
		cl.unlock()
		time.Sleep(time.Millisecond)
	}
	defer cl.unlock()
	for i := range tt.NumPieces() {
		_, err := tt.queuePieceCheck(i)
		qt.Assert(t, qt.IsNil(err))
	}
	st := tt.statsLocked()
	qt.Check(t, qt.Not(qt.Equals(st.PiecesHashing, 0)))
	qt.Check(t, qt.Equals(st.PiecesQueuedForHash+st.PiecesHashing, tt.NumPieces()))
	cst := cl.statsLocked()
	qt.Check(t, qt.Equals(cst.PiecesHashing, st.PiecesHashing))
	qt.Check(t, qt.Equals(cst.PiecesQueuedForHash, st.PiecesQueuedForHash))
}
