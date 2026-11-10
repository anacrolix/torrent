//go:build !windows

package torrentfs_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/fstest"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	torrentfs "github.com/anacrolix/torrent/fs"
	"github.com/anacrolix/torrent/fs/tfstest"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
)

// Runs the shared backend test suite against TorrentFS.IOFS, which exercises the traversal and read
// helpers without needing FUSE.
func TestIOFSSuite(t *testing.T) {
	tfstest.RunFSTestSuite(t, func(t testing.TB, tfs *torrentfs.TorrentFS) (fs.FS, func()) {
		return tfs.IOFS(), func() {}
	})
}

// Checks IOFS against the io/fs contract with a single-file torrent and a nested multi-file torrent
// that are already complete.
func TestIOFSConformance(t *testing.T) {
	cfg := torrent.TestingConfig(t)
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	dataDir := cfg.DataDir

	testutil.CreateDummyTorrentData(dataDir)
	multiFiles := map[string]string{
		"a":         "apple",
		"empty":     "",
		"dir/b":     "banana",
		"dir/sub/c": "cherry",
	}
	multiRoot := filepath.Join(dataDir, "multi")
	for name, data := range multiFiles {
		p := filepath.Join(multiRoot, filepath.FromSlash(name))
		qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(p), 0o755)))
		qt.Assert(t, qt.IsNil(os.WriteFile(p, []byte(data), 0o644)))
	}
	multiInfo := metainfo.Info{PieceLength: 4}
	qt.Assert(t, qt.IsNil(multiInfo.BuildFromFilePath(multiRoot)))

	cl, err := torrent.NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	for _, mi := range []*metainfo.MetaInfo{
		testutil.GreetingMetaInfo(),
		{InfoBytes: bencode.MustMarshal(multiInfo)},
	} {
		tt, err := cl.AddTorrent(mi)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNil(tt.VerifyDataContext(t.Context())))
		// Reads of incomplete data would block forever.
		qt.Assert(t, qt.Equals(tt.BytesMissing(), 0))
	}

	tfs := torrentfs.New(cl)
	defer tfs.Destroy()
	fsys := tfs.IOFS()

	expected := []string{testutil.GreetingFileName}
	for name := range multiFiles {
		expected = append(expected, "multi/"+name)
	}
	qt.Assert(t, qt.IsNil(fstest.TestFS(fsys, expected...)))

	for name, data := range multiFiles {
		b, err := fs.ReadFile(fsys, "multi/"+name)
		qt.Check(t, qt.IsNil(err))
		qt.Check(t, qt.Equals(string(b), data))
	}

	_, err = fsys.Open("nope")
	qt.Check(t, qt.ErrorIs(err, fs.ErrNotExist))
	_, err = fsys.Open("multi/dir/nope")
	qt.Check(t, qt.ErrorIs(err, fs.ErrNotExist))
	_, err = fsys.Open("greeting/x")
	qt.Check(t, qt.ErrorIs(err, syscall.ENOTDIR))
	_, err = fsys.Open("/greeting")
	qt.Check(t, qt.ErrorIs(err, fs.ErrInvalid))

	// Reads on a closed file fail rather than blocking or succeeding.
	f, err := fsys.Open(testutil.GreetingFileName)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(f.Close()))
	_, err = f.Read(make([]byte, 1))
	qt.Check(t, qt.ErrorIs(err, fs.ErrClosed))
}
