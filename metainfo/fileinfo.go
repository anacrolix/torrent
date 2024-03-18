package metainfo

import (
	"strings"

	g "github.com/anacrolix/generics"
)

// Information specific to a single file inside the MetaInfo structure.
type FileInfo struct {
	// BEP3. With BEP 47 this can be optional, but we have no way to describe that without breaking
	// the API.
	Length int64    `bencode:"length"`
	Path   []string `bencode:"path"` // BEP3
	// Unofficial extension by BiglyBT? https://github.com/BiglySoftware/BiglyBT/issues/1274. Might
	// be a safer bet when available: https://github.com/anacrolix/torrent/pull/915.
	PathUtf8 []string `bencode:"path.utf-8,omitempty"`

	ExtendedFileAttrs

	// BEP 52. This isn't encoded in a v1 FileInfo, but is exposed here for APIs that expect to deal
	// v1 files.
	PiecesRoot    g.Option[[32]byte] `bencode:"-"`
	TorrentOffset int64              `bencode:"-"`
}

func (fi *FileInfo) DisplayPath(info *Info) string {
	if info.IsDir() {
		return strings.Join(fi.BestPath(), "/")
	} else {
		return info.BestName()
	}
}

func (fi *FileInfo) BestPath() []string {
	if len(fi.PathUtf8) != 0 {
		return fi.PathUtf8
	}
	return fi.Path
}
