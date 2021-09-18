package metainfo

import "strings"

// Information specific to a single file inside the MetaInfo structure.
type FileInfo struct {
	Length   int64    `bencode:"length"` // BEP3
	Path     []string `bencode:"path"`   // BEP3
	PathUTF8 []string `bencode:"path.utf-8,omitempty"`
}

func (fi *FileInfo) DisplayPath(info *Info) string {
	if info.IsDir() {
		return strings.Join(fi.Path, "/")
	} else {
		return info.Name
	}
}

func (fi *FileInfo) Offset(info *Info) (ret int64) {
	files := info.UpvertedFiles()
	dp := fi.DisplayPath(info)
	for i := 0; i < len(files); i += 1 {
		if dp == files[i].DisplayPath(info) {
			return
		}
		ret += files[i].Length
	}
	panic("not found")
}
