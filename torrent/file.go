package torrent

import (
	"crypto/sha1"
	"errors"
	"github.com/nsf/libtorgo/bencode"
	"io"
	"os"
	"time"
)

//----------------------------------------------------------------------------

// SingleFile represents the specific data of the single file torrent
// metainfo. That includes length of the file and its recommended name.
type SingleFile struct {
	Name   string
	Length int64
}

//----------------------------------------------------------------------------

// MultiFile represents the specific data of the multiple files torrent
// metainfo. That includes the name of the directory which will contain all the
// files and the files information.
type MultiFile struct {
	Name  string
	Files []FileInfo
}

// Information of a single file in multiple files torrent metainfo.
type FileInfo struct {
	Length int64
	Path   []string
}

//----------------------------------------------------------------------------

// MetaInfo is the type you should use when reading torrent files. See Load and
// LoadFromFile functions. All the fields are intended to be read-only. The
// "Info" field has SingleFile or MultiFile type, use the type switch or type
// assertion to determine the exact type.
type MetaInfo struct {
	Info        interface{}
	InfoHash    []byte
	PieceLength int64
	Pieces      []byte
	Private     bool

	AnnounceList [][]string
	CreationDate time.Time
	Comment      string
	CreatedBy    string
	Encoding     string
	URLList      []string
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

	// post-parse processing
	if len(data.Info.Files) > 0 {
		files := make([]FileInfo, len(data.Info.Files))
		for i, fi := range data.Info.Files {
			files[i] = FileInfo{
				Length: fi.Length,
				Path:   fi.Path,
			}
		}
		mi.Info = MultiFile{
			Name:  data.Info.Name,
			Files: files,
		}
	} else {
		mi.Info = SingleFile{
			Name:   data.Info.Name,
			Length: data.Info.Length,
		}
	}
	mi.InfoHash = data.Info.Hash
	mi.PieceLength = data.Info.PieceLength
	mi.Pieces = data.Info.Pieces
	mi.Private = data.Info.Private

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
			mi.URLList = []string{v}
		case []interface{}:
			var ok bool
			ss := make([]string, len(v))
			for i, s := range v {
				ss[i], ok = s.(string)
				if !ok {
					return nil, errors.New("bad url-list data type")
				}
			}
			mi.URLList = ss
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

//----------------------------------------------------------------------------
// unmarshal structures
//----------------------------------------------------------------------------

type torrent_info_file struct {
	Path   []string `bencode:"path"`
	Length int64    `bencode:"length"`
}

type torrent_info struct {
	PieceLength int64               `bencode:"piece length"`
	Pieces      []byte              `bencode:"pieces"`
	Name        string              `bencode:"name"`
	Length      int64               `bencode:"length,omitempty"`
	Private     bool                `bencode:"private,omitempty"`
	Files       []torrent_info_file `bencode:"files,omitempty"`
}

type torrent_info_ex struct {
	torrent_info
	Hash []byte
}

func (this *torrent_info_ex) UnmarshalBencode(data []byte) error {
	h := sha1.New()
	h.Write(data)
	this.Hash = h.Sum(this.Hash)
	return bencode.Unmarshal(data, &this.torrent_info)
}

func (this *torrent_info_ex) MarshalBencode() ([]byte, error) {
	return bencode.Marshal(&this.torrent_info)
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
