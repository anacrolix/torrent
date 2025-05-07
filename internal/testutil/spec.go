package testutil

import (
	"io"
	"strings"

	"github.com/anacrolix/missinggo/expect"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

type File struct {
	Name string
	Data string
}

// High-level description of a torrent for testing purposes.
type Torrent struct {
	Files []File
	Name  string
}

func (t *Torrent) IsDir() bool {
	return len(t.Files) == 1 && t.Files[0].Name == ""
}

func (t *Torrent) GetFile(name string) *File {
	if t.IsDir() && t.Name == name {
		return &t.Files[0]
	}
	for _, f := range t.Files {
		if f.Name == name {
			return &f
		}
	}
	return nil
}

func (t *Torrent) Info(pieceLength int64) metainfo.Info {
	info := metainfo.Info{
		Name:        t.Name,
		PieceLength: pieceLength,
	}
	if t.IsDir() {
		info.Length = int64(len(t.Files[0].Data))
	} else {
		for _, f := range t.Files {
			info.Files = append(info.Files, metainfo.FileInfo{
				Path:   []string{f.Name},
				Length: int64(len(f.Data)),
			})
		}
	}
	err := info.GeneratePieces(func(fi metainfo.FileInfo) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(t.GetFile(strings.Join(fi.BestPath(), "/")).Data)), nil
	})
	expect.Nil(err)
	return info
}

// Create an info and metainfo with bytes set for the torrent with the provided piece length.
func (t *Torrent) Generate(pieceLength int64) (mi metainfo.MetaInfo, info metainfo.Info) {
	var err error
	info = t.Info(pieceLength)
	mi.InfoBytes, err = bencode.Marshal(info)
	expect.Nil(err)
	return
}
