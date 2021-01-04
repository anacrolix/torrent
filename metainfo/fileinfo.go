package metainfo

import "strings"

// FileInfo information specific to a single file inside the MetaInfo structure.
type FileInfo struct {
	Length   int64    `bencode:"length"`
	Path     []string `bencode:"path"`
	PathUTF8 []string `bencode:"path.utf-8,omitempty"`
}

// DisplayPath ...
func (fi *FileInfo) DisplayPath(info *Info) string {
	if info.IsDir() {
		return strings.Join(fi.Path, "/")
	}

	return info.Name
}

// Offset ...
func (fi FileInfo) Offset(info *Info) (ret int64) {
	for _, c := range info.UpvertedFiles() {
		if fi.DisplayPath(info) == c.DisplayPath(info) {
			return
		}
		ret += fi.Length
	}
	panic("not found")
}
