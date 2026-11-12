package torrent

import (
	"crypto/sha256"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Empty files in a v2 file tree sit at the offset of the next piece boundary. They must not count
// towards the files of the piece before them, which for v2 must be exactly one.
func TestAddV2TorrentWithEmptyFiles(t *testing.T) {
	const pieceLength = 1 << 14
	root := sha256.Sum256(make([]byte, pieceLength))
	file := metainfo.FileTree{File: metainfo.FileTreeFile{Length: pieceLength, PiecesRoot: string(root[:])}}
	empty := metainfo.FileTree{File: metainfo.FileTreeFile{Length: 0}}
	for name, tree := range map[string]map[string]metainfo.FileTree{
		"after the last piece":   {"a": file, "b": empty, "c": empty},
		"between pieces":         {"a": file, "b": empty, "c": empty, "d": file},
		"before the first piece": {"a": empty, "b": file},
	} {
		t.Run(name, func(t *testing.T) {
			info := metainfo.Info{
				Name:        "x",
				PieceLength: pieceLength,
				MetaVersion: 2,
				FileTree:    metainfo.FileTree{Dir: tree},
			}
			ib, err := bencode.Marshal(&info)
			qt.Assert(t, qt.IsNil(err))
			cfg := TestingConfig(t)
			cfg.DefaultStorage = storage.NewFile(cfg.DataDir)
			cl, err := NewClient(cfg)
			qt.Assert(t, qt.IsNil(err))
			defer cl.Close()
			tt, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: ib})
			qt.Assert(t, qt.IsNil(err))
			<-tt.GotInfo()
			for i := range tt.NumPieces() {
				qt.Check(t, qt.Equals(tt.Piece(i).numFiles(), 1), qt.Commentf("piece %d", i))
			}
		})
	}
}
