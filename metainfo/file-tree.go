package metainfo

import (
	"sort"

	g "github.com/anacrolix/generics"
	"golang.org/x/exp/maps"

	"github.com/anacrolix/torrent/bencode"
)

const FileTreePropertiesKey = ""

type FileTreeFile struct {
	Length     int64  `bencode:"length"`
	PiecesRoot string `bencode:"pieces root"`
}

// The fields here don't need bencode tags as the marshalling is done manually.
type FileTree struct {
	File FileTreeFile
	Dir  map[string]FileTree
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

func (ft *FileTree) MarshalBencode() (bytes []byte, err error) {
	if ft.IsDir() {
		dir := make(map[string]bencode.Bytes, len(ft.Dir))
		for _, key := range ft.orderedKeys() {
			if key == FileTreePropertiesKey {
				continue
			}
			sub := g.MapMustGet(ft.Dir, key)
			subBytes, err := sub.MarshalBencode()
			if err != nil {
				return nil, err
			}
			dir[key] = subBytes
		}
		return bencode.Marshal(dir)
	} else {
		fileBytes, err := bencode.Marshal(ft.File)
		if err != nil {
			return nil, err
		}
		res := map[string]bencode.Bytes{
			"": fileBytes,
		}
		return bencode.Marshal(res)
	}
}

var _ bencode.Marshaler = (*FileTree)(nil)

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

func (ft *FileTree) upvertedFiles(pieceLength int64, out func(fi FileInfo)) {
	var offset int64
	ft.upvertedFilesInner(pieceLength, nil, &offset, out)
}

func (ft *FileTree) upvertedFilesInner(
	pieceLength int64,
	path []string,
	offset *int64,
	out func(fi FileInfo),
) {
	if ft.IsDir() {
		for _, key := range ft.orderedKeys() {
			if key == FileTreePropertiesKey {
				continue
			}
			sub := g.MapMustGet(ft.Dir, key)
			sub.upvertedFilesInner(pieceLength, append(path, key), offset, out)
		}
	} else {
		out(FileInfo{
			Length: ft.File.Length,
			Path:   append([]string(nil), path...),
			// BEP 52 requires paths be UTF-8 if possible.
			PathUtf8:      append([]string(nil), path...),
			PiecesRoot:    ft.PiecesRootAsByteArray(),
			TorrentOffset: *offset,
		})
		*offset += (ft.File.Length + pieceLength - 1) / pieceLength * pieceLength
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
	if ft.File.PiecesRoot == "" {
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
