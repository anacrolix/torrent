package metainfo

import (
	"crypto/sha1"
	"github.com/nsf/libtorgo/bencode"
	"io"
	"os"
)

// Information specific to a single file inside the MetaInfo structure.
type FileInfo struct {
	Length int64    `bencode:"length"`
	Path   []string `bencode:"path"`
}

// Load a MetaInfo from an io.Reader. Returns a non-nil error in case of
// failure.
func Load(r io.Reader) (*MetaInfo, error) {
	var mi MetaInfo
	d := bencode.NewDecoder(r)
	err := d.Decode(&mi)
	if err != nil {
		return nil, err
	}
	return &mi, nil
}

// Convenience function for loading a MetaInfo from a file.
func LoadFromFile(filename string) (*MetaInfo, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}

// The info dictionary.
type Info struct {
	PieceLength int64      `bencode:"piece length"`
	Pieces      []byte     `bencode:"pieces"`
	Name        string     `bencode:"name"`
	Length      int64      `bencode:"length,omitempty"`
	Private     bool       `bencode:"private,omitempty"`
	Files       []FileInfo `bencode:"files,omitempty"`
}

// The info dictionary with its hash and raw bytes exposed, as these are
// important to Bittorrent.
type InfoEx struct {
	Info
	Hash  []byte
	Bytes []byte
}

func (this *InfoEx) UnmarshalBencode(data []byte) error {
	this.Bytes = make([]byte, 0, len(data))
	this.Bytes = append(this.Bytes, data...)
	h := sha1.New()
	h.Write(this.Bytes)
	this.Hash = h.Sum(this.Hash)
	return bencode.Unmarshal(data, &this.Info)
}

func (this *InfoEx) MarshalBencode() ([]byte, error) {
	return bencode.Marshal(&this.Info)
}

type MetaInfo struct {
	Info         InfoEx      `bencode:"info"`
	Announce     string      `bencode:"announce"`
	AnnounceList [][]string  `bencode:"announce-list,omitempty"`
	CreationDate int64       `bencode:"creation date,omitempty"`
	Comment      string      `bencode:"comment,omitempty"`
	CreatedBy    string      `bencode:"created by,omitempty"`
	Encoding     string      `bencode:"encoding,omitempty"`
	URLList      interface{} `bencode:"url-list,omitempty"`
}
