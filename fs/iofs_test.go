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

// Malformed metadata can list a path as both a file and a directory prefix of another file. The
// client accepts it (the file list comes from a remote peer over ut_metadata), so the io/fs
// traversal must not panic. The file wins and the shadowed descendants are not reachable, matching
// how the directory listing presents them.
func TestIOFSDirFileConflict(t *testing.T) {
	addConflict := func(t *testing.T, paths ...[]string) fs.FS {
		var files []any
		for _, p := range paths {
			comps := make([]any, len(p))
			for i, c := range p {
				comps[i] = c
			}
			files = append(files, map[string]any{"length": int64(1), "path": comps})
		}
		ib, err := bencode.Marshal(map[string]any{
			"name": "root", "piece length": int64(16384),
			"pieces": string(make([]byte, 20)), "files": files,
		})
		qt.Assert(t, qt.IsNil(err))
		cfg := torrent.TestingConfig(t)
		cfg.DisableTCP = true
		cfg.DisableUTP = true
		cl, err := torrent.NewClient(cfg)
		qt.Assert(t, qt.IsNil(err))
		t.Cleanup(func() { cl.Close() })
		tt, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: ib})
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNotNil(tt.Info())) // accepted despite the conflict
		tfs := torrentfs.New(cl)
		t.Cleanup(tfs.Destroy)
		return tfs.IOFS()
	}

	// The conflicting name resolves to the file in each of these shapes, and the shadowed child is
	// not reachable through it. The listing and the lookup agree, and a full walk never panics.
	for _, c := range []struct {
		name  string
		paths [][]string
	}{
		{"file before subdir", [][]string{{"a"}, {"a", "b"}}},
		{"subdir before file", [][]string{{"a", "b"}, {"a"}}},
		{"deeply nested child", [][]string{{"a"}, {"a", "b", "c"}}},
		{"multiple shadowed children", [][]string{{"a"}, {"a", "b"}, {"a", "c"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			iofs := addConflict(t, c.paths...)

			des, err := fs.ReadDir(iofs, "root")
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.HasLen(des, 1))
			qt.Check(t, qt.Equals(des[0].Name(), "a"))
			qt.Check(t, qt.IsFalse(des[0].IsDir()))

			st, err := fs.Stat(iofs, "root/a")
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.IsFalse(st.IsDir()))

			_, err = iofs.Open("root/a/b")
			qt.Check(t, qt.IsNotNil(err))

			qt.Check(t, qt.IsNil(fs.WalkDir(iofs, "root", func(string, fs.DirEntry, error) error { return nil })))
		})
	}

	// A real directory (no file shares the name) still lists and resolves as a directory.
	t.Run("plain directory still works", func(t *testing.T) {
		iofs := addConflict(t, []string{"d", "x"}, []string{"d", "y"})
		st, err := fs.Stat(iofs, "root/d")
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.IsTrue(st.IsDir()))
		des, err := fs.ReadDir(iofs, "root/d")
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.HasLen(des, 2))
	})
}
