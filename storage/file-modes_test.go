//go:build !windows

package storage

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-quicktest/qt"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
)

func promoteTestFile(t *testing.T, opts NewFileClientOpts) os.FileMode {
	dir := t.TempDir()
	final := filepath.Join(dir, "a.bin")
	qt.Assert(t, qt.IsNil(os.WriteFile(final+".part", []byte{1}, 0o640)))
	qt.Assert(t, qt.IsNil(os.Chmod(final+".part", 0o640)))
	opts.UsePartFiles = g.Some(true)
	opts.Logger = slog.Default()
	torrent := &fileTorrentImpl{
		info:              &metainfo.Info{PieceLength: 1},
		files:             []fileExtra{{safeOsPath: final}},
		metainfoFileInfos: []metainfo.FileInfo{{Path: []string{"a.bin"}, Length: 1}},
		io:                opts.fileIo(),
		client:            &fileClientImpl{opts: opts},
	}
	defer torrent.io.Close()
	qt.Assert(t, qt.IsNil((&filePieceImpl{t: torrent}).promotePartFile(torrent.file(0))))
	fi, err := os.Stat(final)
	qt.Assert(t, qt.IsNil(err))
	return fi.Mode().Perm()
}

func TestPromotePartFileMakesFileReadOnlyByDefault(t *testing.T) {
	qt.Check(t, qt.Equals(promoteTestFile(t, NewFileClientOpts{}), filePerm&^0o222))
}

func TestPromotePartFileKeepsModeWhenAsked(t *testing.T) {
	qt.Check(t, qt.Equals(promoteTestFile(t, NewFileClientOpts{KeepFileModes: true}), os.FileMode(0o640)))
}

func TestOpenFileExtraUsesConfiguredModes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "a.bin")
	f, err := openFileExtra(p, os.O_RDWR, fileModes{file: 0o600, dir: 0o700})
	qt.Assert(t, qt.IsNil(err))
	f.Close()
	fi, err := os.Stat(p)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(fi.Mode().Perm(), os.FileMode(0o600)))
	di, err := os.Stat(filepath.Dir(p))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(di.Mode().Perm(), os.FileMode(0o700)))
}

func TestOpenFileExtraDoesNotChmodWhenKeepingModes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open read-only files for writing")
	}
	p := filepath.Join(t.TempDir(), "a.bin")
	qt.Assert(t, qt.IsNil(os.WriteFile(p, nil, 0o444)))
	_, err := openFileExtra(p, os.O_RDWR, fileModes{file: filePerm, dir: dirPerm, keep: true})
	qt.Check(t, qt.ErrorIs(err, os.ErrPermission))
	fi, err := os.Stat(p)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(fi.Mode().Perm(), os.FileMode(0o444)))
}
