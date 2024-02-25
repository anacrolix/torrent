package metainfo

type FileTree struct {
	File struct {
		Length     int64  `bencode:"length"`
		PiecesRoot string `bencode:"pieces root"`
	}
	Dir map[string]FileTree
}
