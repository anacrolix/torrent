package torrent

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
)

// v2FileHashes returns the pieces root of a file and, for a file longer than one piece, its piece
// layer.
func v2FileHashes(data []byte, pieceLength int) (root [32]byte, layer []byte) {
	if len(data) <= pieceLength {
		h := merkle.NewHash()
		h.Write(data)
		copy(root[:], h.Sum(nil))
		return
	}
	var pieceHashes [][32]byte
	for off := 0; off < len(data); off += pieceLength {
		h := merkle.NewHash()
		h.Write(data[off:min(off+pieceLength, len(data))])
		var ph [32]byte
		copy(ph[:], h.SumMinLength(nil, pieceLength))
		pieceHashes = append(pieceHashes, ph)
		layer = append(layer, ph[:]...)
	}
	padPiece := merkle.RootWithPadHash(make([][32]byte, pieceLength/merkle.BlockSize), [32]byte{})
	root = merkle.RootWithPadHash(pieceHashes, padPiece)
	return
}

// In a v2 torrent every file starts on a piece boundary, so a file that isn't a multiple of the
// piece length ends in a short piece even when it isn't the last file. Webseed requests used to
// assume only the last piece can be short: they ran past the end of such a piece and panicked, and
// they stopped at the torrent's length in chunks, which leaves out the tail of the request index
// space that the padding adds.
func TestWebseedDownloadsV2TorrentWithShortPiecesBetweenFiles(t *testing.T) {
	const pieceLen = 4 * defaultChunkSize

	files := []struct {
		name string
		data []byte
	}{
		{"a.bin", make([]byte, defaultChunkSize+3000)},
		{"b.bin", make([]byte, 3*pieceLen+5000)},
	}
	dir := t.TempDir()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "v2web"), 0o755)))
	tree := metainfo.FileTree{Dir: map[string]metainfo.FileTree{}}
	pieceLayers := map[string]string{}
	for _, f := range files {
		rand.Read(f.data)
		qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "v2web", f.name), f.data, 0o644)))
		root, layer := v2FileHashes(f.data, pieceLen)
		tree.Dir[f.name] = metainfo.FileTree{File: metainfo.FileTreeFile{
			Length:     int64(len(f.data)),
			PiecesRoot: string(root[:]),
		}}
		if layer != nil {
			pieceLayers[string(root[:])] = string(layer)
		}
	}
	infoBytes, err := bencode.Marshal(&metainfo.Info{
		Name:        "v2web",
		PieceLength: pieceLen,
		MetaVersion: 2,
		FileTree:    tree,
	})
	qt.Assert(t, qt.IsNil(err))

	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	mi := &metainfo.MetaInfo{
		InfoBytes:   infoBytes,
		PieceLayers: pieceLayers,
		UrlList:     []string{srv.URL + "/"},
	}

	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	tt.DownloadAll()

	done := make(chan bool)
	go func() { done <- cl.WaitAll() }()
	select {
	case ok := <-done:
		qt.Assert(t, qt.IsTrue(ok))
	case <-time.After(10 * time.Second):
		t.Fatalf("download stalled at %v of %v bytes", tt.BytesCompleted(), tt.Length())
	}

	qt.Assert(t, qt.HasLen(tt.Files(), len(files)))
	for i, tf := range tt.Files() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		r := tf.NewReader()
		r.SetContext(ctx)
		got, err := io.ReadAll(r)
		r.Close()
		cancel()
		qt.Assert(t, qt.IsNil(err), qt.Commentf("reading %v", tf.DisplayPath()))
		qt.Check(t, qt.DeepEquals(got, files[i].data), qt.Commentf("file %v", tf.DisplayPath()))
	}
}
