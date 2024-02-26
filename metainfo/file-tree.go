package metainfo

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/bencode"
	"golang.org/x/exp/maps"
	"sort"
)

const FileTreePropertiesKey = ""

type FileTree struct {
	File struct {
		Length     int64  `bencode:"length"`
		PiecesRoot string `bencode:"pieces root"`
	}
	Dir map[string]FileTree
}

func (ft *FileTree) UnmarshalBencode(bytes []byte) (err error) {
	var dir map[string]bencode.Bytes
	err = bencode.Unmarshal(bytes, &dir)
	if err != nil {
		return
	}
	if propBytes, ok := dir[""]; ok {
		err = bencode.Unmarshal(propBytes, &ft.File)
		if err != nil {
			return
		}
	}
	delete(dir, "")
	g.MakeMapWithCap(&ft.Dir, len(dir))
	for key, bytes := range dir {
		var sub FileTree
		err = sub.UnmarshalBencode(bytes)
		if err != nil {
			return
		}
		ft.Dir[key] = sub
	}
	return
}

var _ bencode.Unmarshaler = (*FileTree)(nil)

func (ft *FileTree) NumEntries() (num int) {
	num = len(ft.Dir)
	if g.MapContains(ft.Dir, FileTreePropertiesKey) {
		num--
	}
	return
}

func (ft *FileTree) IsDir() bool {
	return ft.NumEntries() != 0
}

func (ft *FileTree) orderedKeys() []string {
	keys := maps.Keys(ft.Dir)
	sort.Strings(keys)
	return keys
}

func (ft *FileTree) UpvertedFiles(path []string, out func(fi FileInfo)) {
	if ft.IsDir() {
		for _, key := range ft.orderedKeys() {
			if key == FileTreePropertiesKey {
				continue
			}
			sub := g.MapMustGet(ft.Dir, key)
			sub.UpvertedFiles(append(path, key), out)
		}
	} else {
		out(FileInfo{
			Length: ft.File.Length,
			Path:   append([]string(nil), path...),
			// BEP 52 requires paths be UTF-8 if possible.
			PathUtf8:   append([]string(nil), path...),
			PiecesRoot: ft.PiecesRootAsByteArray(),
		})
	}
}

func (ft *FileTree) Walk(path []string, f func(path []string, ft *FileTree)) {
	f(path, ft)
	for key, sub := range ft.Dir {
		if key == FileTreePropertiesKey {
			continue
		}
		sub.Walk(append(path, key), f)
	}
}

func (ft *FileTree) PiecesRootAsByteArray() (ret g.Option[[32]byte]) {
	if ft.File.Length == 0 {
		return
	}
	n := copy(ret.Value[:], ft.File.PiecesRoot)
	if n != 32 {
		// Must be 32 bytes for meta version 2 and non-empty files. See BEP 52.
		panic(n)
	}
	ret.Ok = true
	return
}
