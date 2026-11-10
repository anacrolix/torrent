package torrent

import (
	"os"
	"path/filepath"
	"testing"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
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

// Bad piece layers fail the add after the storage is open but before the piece request order
// exists. Dropping the half-added torrent must not panic, nor must closing the client after.
func TestAddV2TorrentWithBadPieceLayers(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	for root, layer := range mi.PieceLayers {
		mi.PieceLayers[root] = layer[:len(layer)-1]
	}

	cfg := TestingConfig(t)
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: t.TempDir()})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	_, err = cl.AddTorrent(mi)
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.HasLen(cl.Torrents(), 0))
	cl.Close()
}

// A v2 torrent may legally end with a zero-length file. That empty file shares the final piece's
// end offset, so the piece maps to it as well as the real file. Setting such info must not trip the
// "a v2 piece belongs to one file" assumption (it used to panic "2 != 1"). This info arrives
// straight from peers over ut_metadata, so it's remotely reachable.
func TestAddV2TorrentWithTrailingEmptyFile(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	// Append an empty file after the existing data.bin in the file tree, keeping every other info
	// field byte-identical.
	var fields map[string]bencode.Bytes
	qt.Assert(t, qt.IsNil(bencode.Unmarshal(mi.InfoBytes, &fields)))
	var ft metainfo.FileTree
	qt.Assert(t, qt.IsNil(ft.UnmarshalBencode(fields["file tree"])))
	ft.Dir["zz-empty.bin"] = metainfo.FileTree{File: metainfo.FileTreeFile{Length: 0}}
	ftBytes, err := ft.MarshalBencode()
	qt.Assert(t, qt.IsNil(err))
	fields["file tree"] = ftBytes
	infoBytes := bencode.MustMarshal(fields)

	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHashV2: g.Some(infohash_v2.HashBytes(infoBytes))})
	qt.Assert(t, qt.IsNil(tt.SetInfoBytes(infoBytes)))
	qt.Check(t, qt.HasLen(tt.Info().UpvertedFiles(), 2))
}

// The same torrent with real data on disk must still hash, verify and complete, so the changed
// one-file-per-piece logic is exercised on the storage path too.
func TestV2TorrentTrailingEmptyFileVerifies(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	var fields map[string]bencode.Bytes
	qt.Assert(t, qt.IsNil(bencode.Unmarshal(mi.InfoBytes, &fields)))
	var ft metainfo.FileTree
	qt.Assert(t, qt.IsNil(ft.UnmarshalBencode(fields["file tree"])))
	ft.Dir["zz-empty.bin"] = metainfo.FileTree{File: metainfo.FileTreeFile{Length: 0}}
	ftBytes, err := ft.MarshalBencode()
	qt.Assert(t, qt.IsNil(err))
	fields["file tree"] = ftBytes
	mi.InfoBytes = bencode.MustMarshal(fields)

	data, err := os.ReadFile("testdata/v2-multi-piece/data.bin")
	qt.Assert(t, qt.IsNil(err))
	dir := t.TempDir()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "v2multi"), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "v2multi", "data.bin"), data, 0o644)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "v2multi", "zz-empty.bin"), nil, 0o644)))

	cfg := TestingConfig(t)
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	qt.Assert(t, qt.IsNil(tt.VerifyData()))
	qt.Check(t, qt.Equals(tt.BytesCompleted(), tt.Length()))
	qt.Check(t, qt.HasLen(tt.Info().UpvertedFiles(), 2))
}
