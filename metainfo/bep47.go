package metainfo

// See BEP 47. This is common to both Info and FileInfo.
type ExtendedFileAttrs struct {
	Attr        string   `bencode:"attr,omitempty"`
	SymlinkPath []string `bencode:"symlink path,omitempty"`
	Sha1        string   `bencode:"sha1,omitempty"`
}
