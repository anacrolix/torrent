package metainfo

import "strings"

// Information specific to a single file inside the MetaInfo structure.
type FileInfo struct {
	Length   int64    `bencode:"length"` // BEP3
	Path     []string `bencode:"path"`   // BEP3
	PathUtf8 []string `bencode:"path.utf-8,omitempty"`
}

func (fi *FileInfo) DisplayPath(info *Info) string {
	if info.IsDir() {
		return strings.Join(fi.BestPath(), "/")
	} else {
		return info.BestName()
	}
}

func (me FileInfo) Offset(info *Info) (ret int64) {
	for _, fi := range info.UpvertedFiles() {
		if me.DisplayPath(info) == fi.DisplayPath(info) {
			return
		}
		ret += fi.Length
	}
	panic("not found")
}

func (fi FileInfo) BestPath() []string {
	if len(fi.PathUtf8) != 0 {
		return fi.PathUtf8
	}
	return fi.Path
}
