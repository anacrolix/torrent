package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(link), 0o750)))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("can't create symlink: %v", err)
	}
}

func TestOpenTorrentSymlinks(t *testing.T) {
	info := multiFileInfo(
		metainfo.FileInfo{Path: []string{"d", "a"}, Length: 3},
		metainfo.FileInfo{Path: []string{"empty"}, Length: 0},
	)
	for _, tc := range []struct {
		name string
		// Sets up dir, the client base dir, and outside, a dir beside it.
		setup func(t *testing.T, dir, outside string)
		ok    bool
	}{
		{
			name:  "NoLinks",
			setup: func(*testing.T, string, string) {},
			ok:    true,
		},
		{
			name: "DirLinkOutside",
			setup: func(t *testing.T, dir, outside string) {
				symlinkOrSkip(t, outside, filepath.Join(dir, "t", "d"))
			},
		},
		{
			name: "FileLinkOutside",
			setup: func(t *testing.T, dir, outside string) {
				writeTestFile(t, filepath.Join(outside, "victim"), "keep me")
				symlinkOrSkip(t, filepath.Join(outside, "victim"), filepath.Join(dir, "t", "empty"))
			},
		},
		{
			name: "PartFileLinkOutside",
			setup: func(t *testing.T, dir, outside string) {
				writeTestFile(t, filepath.Join(outside, "victim"), "keep me")
				symlinkOrSkip(t, filepath.Join(outside, "victim"), filepath.Join(dir, "t", "d", "a.part"))
			},
		},
		{
			name: "DanglingLink",
			setup: func(t *testing.T, dir, outside string) {
				symlinkOrSkip(t, filepath.Join(outside, "missing"), filepath.Join(dir, "t", "empty"))
			},
		},
		{
			name: "TorrentDirLinkOutside",
			setup: func(t *testing.T, dir, outside string) {
				symlinkOrSkip(t, outside, filepath.Join(dir, "t"))
			},
		},
		{
			name: "DirLinkInside",
			setup: func(t *testing.T, dir, outside string) {
				qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "t", "real"), 0o750)))
				symlinkOrSkip(t, "real", filepath.Join(dir, "t", "d"))
			},
			ok: true,
		},
		{
			name: "BaseDirIsLink",
			setup: func(t *testing.T, dir, outside string) {
				qt.Assert(t, qt.IsNil(os.Remove(dir)))
				symlinkOrSkip(t, outside, dir)
			},
			ok: true,
		},
		{
			name: "NotARegularFile",
			setup: func(t *testing.T, dir, outside string) {
				qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "t", "empty"), 0o750)))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "downloads")
			outside := filepath.Join(parent, "outside")
			qt.Assert(t, qt.IsNil(os.Mkdir(dir, 0o750)))
			qt.Assert(t, qt.IsNil(os.Mkdir(outside, 0o750)))
			tc.setup(t, dir, outside)
			_, err := openFileStorageForTest(t, dir, info, true)
			if tc.ok {
				qt.Assert(t, qt.IsNil(err))
			} else {
				qt.Assert(t, qt.IsNotNil(err))
			}
			if _, statErr := os.Stat(filepath.Join(outside, "victim")); statErr == nil {
				assertFileContents(t, filepath.Join(outside, "victim"), "keep me")
			}
		})
	}
}

func TestFollowSymlinksOutOfTorrentDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "downloads")
	outside := filepath.Join(parent, "outside")
	qt.Assert(t, qt.IsNil(os.Mkdir(outside, 0o750)))
	symlinkOrSkip(t, outside, filepath.Join(dir, "t", "d"))
	cl := NewFileOpts(NewFileClientOpts{
		ClientBaseDir:                 dir,
		UsePartFiles:                  g.Some(true),
		FollowSymlinksOutOfTorrentDir: true,
	})
	defer cl.Close()
	ts, err := cl.OpenTorrent(
		context.Background(),
		multiFileInfo(metainfo.FileInfo{Path: []string{"d", "a"}, Length: 3}),
		metainfo.Hash{})
	qt.Assert(t, qt.IsNil(err))
	ts.Close()
}
