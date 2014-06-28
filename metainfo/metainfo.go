package metainfo

import (
	"crypto/sha1"
	"errors"
	"github.com/nsf/libtorgo/bencode"
	"io"
	"os"
	"time"
)

// Information specific to a single file inside the MetaInfo structure..
type FileInfo struct {
	Length int64    `bencode:"length"`
	Path   []string `bencode:"path"`
}

// MetaInfo is the type you should use when reading torrent files. See Load and
// LoadFromFile functions. All the fields are intended to be read-only. If
// 'len(Files) == 1', then the FileInfo.Path is nil in that entry.
type MetaInfo struct {
	Info
	InfoHash     []byte
	AnnounceList [][]string
	CreationDate time.Time
	Comment      string
	CreatedBy    string
	Encoding     string
	WebSeedURLs  []string
	InfoBytes    []byte
}

// Load a MetaInfo from an io.Reader. Returns a non-nil error in case of
// failure.
func Load(r io.Reader) (*MetaInfo, error) {
	var mi MetaInfo
	var data torrent_data
	d := bencode.NewDecoder(r)
	err := d.Decode(&data)
	if err != nil {
		return nil, err
	}

	mi.Info = data.Info.Info
	mi.InfoBytes = data.Info.Bytes
	mi.InfoHash = data.Info.Hash
	if len(data.AnnounceList) > 0 {
		mi.AnnounceList = data.AnnounceList
	} else {
		mi.AnnounceList = [][]string{[]string{data.Announce}}
	}
	mi.CreationDate = time.Unix(data.CreationDate, 0)
	mi.Comment = data.Comment
	mi.CreatedBy = data.CreatedBy
	mi.Encoding = data.Encoding
	if data.URLList != nil {
		switch v := data.URLList.(type) {
		case string:
			mi.WebSeedURLs = []string{v}
		case []interface{}:
			var ok bool
			ss := make([]string, len(v))
			for i, s := range v {
				ss[i], ok = s.(string)
				if !ok {
					return nil, errors.New("bad url-list data type")
				}
			}
			mi.WebSeedURLs = ss
		default:
			return nil, errors.New("bad url-list data type")
		}
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

type Info struct {
	PieceLength int64      `bencode:"piece length"`
	Pieces      []byte     `bencode:"pieces"`
	Name        string     `bencode:"name"`
	Length      int64      `bencode:"length,omitempty"`
	Private     bool       `bencode:"private,omitempty"`
	Files       []FileInfo `bencode:"files,omitempty"`
}

//----------------------------------------------------------------------------
// unmarshal structures
//----------------------------------------------------------------------------

type torrent_info_ex struct {
	Info
	Hash  []byte
	Bytes []byte
}

func (this *torrent_info_ex) UnmarshalBencode(data []byte) error {
	this.Bytes = make([]byte, 0, len(data))
	this.Bytes = append(this.Bytes, data...)
	h := sha1.New()
	h.Write(this.Bytes)
	this.Hash = h.Sum(this.Hash)
	return bencode.Unmarshal(data, &this.Info)
}

func (this *torrent_info_ex) MarshalBencode() ([]byte, error) {
	return bencode.Marshal(&this.Info)
}

type torrent_data struct {
	Info         torrent_info_ex `bencode:"info"`
	Announce     string          `bencode:"announce"`
	AnnounceList [][]string      `bencode:"announce-list,omitempty"`
	CreationDate int64           `bencode:"creation date,omitempty"`
	Comment      string          `bencode:"comment,omitempty"`
	CreatedBy    string          `bencode:"created by,omitempty"`
	Encoding     string          `bencode:"encoding,omitempty"`
	URLList      interface{}     `bencode:"url-list,omitempty"`
}
