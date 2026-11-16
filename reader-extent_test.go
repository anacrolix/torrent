package torrent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	qt "github.com/go-quicktest/qt"
)

// A reader from File.NewReader must return only that file's bytes, even when
// the caller's buffer is longer than what remains of the file and the next
// file shares the piece.
func TestFileReaderStopsAtFileEnd(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "t")
	qt.Assert(t, qt.IsNil(os.MkdirAll(root, 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "a.txt"), []byte("small"), 0o644)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "b.txt"), bytes.Repeat([]byte("B"), 100_000), 0o644)))
	var info metainfo.Info
	qt.Assert(t, qt.IsNil(info.BuildFromFilePath(root)))
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))

	cfg := TestingConfig(t)
	cfg.DataDir = dataDir
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tor, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: infoBytes})
	qt.Assert(t, qt.IsNil(err))
	<-tor.GotInfo()
	for deadline := time.Now().Add(10 * time.Second); tor.BytesMissing() > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("local data never verified: %d bytes missing", tor.BytesMissing())
		}
	}

	f := tor.Files()[0]
	qt.Assert(t, qt.Equals(f.DisplayPath(), "a.txt"))

	r := f.NewReader()
	defer r.Close()
	got, err := io.ReadAll(r)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(got, []byte("small")))

	// A single read with a buffer longer than the whole file.
	r2 := f.NewReader()
	defer r2.Close()
	buf := make([]byte, 64)
	n, err := r2.Read(buf)
	qt.Assert(t, qt.Equals(n, 5))
	qt.Assert(t, qt.DeepEquals(buf[:n], []byte("small")))
	if err != nil {
		qt.Assert(t, qt.Equals(err, io.EOF))
	}

	// Seek near the end, then read past it.
	r3 := f.NewReader()
	defer r3.Close()
	_, err = r3.Seek(3, io.SeekStart)
	qt.Assert(t, qt.IsNil(err))
	n, err = r3.Read(buf)
	qt.Assert(t, qt.Equals(n, 2))
	qt.Assert(t, qt.DeepEquals(buf[:n], []byte("ll")))
	if err != nil {
		qt.Assert(t, qt.Equals(err, io.EOF))
	}
}
