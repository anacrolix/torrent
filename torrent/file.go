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

type SingleFile struct {
	Name   string
	Length int64
}

//----------------------------------------------------------------------------

type MultiFile struct {
	Name  string
	Files []FileInfo
}

type FileInfo struct {
	Length int64
	Path   []string
}

//----------------------------------------------------------------------------

type File struct {
	// this will be returned as SingleFile or MultiFile, see Info method
	name   string
	length int64
	files  []FileInfo

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

// the real type of this return value is SingleFile or MultiFile and it must be
// checked by an API user
func (f *File) Info() interface{} {
	if len(f.files) > 0 {
		return MultiFile{Name: f.name, Files: f.files}
	}
	return SingleFile{Name: f.name, Length: f.length}
}

func Open(r io.Reader) (*File, error) {
	var file File
	var data torrent_data
	d := bencode.NewDecoder(r)
	err := d.Decode(&data)
	if err != nil {
		return nil, err
	}

	// post-parse processing
	file.name = data.Info.Name
	file.length = data.Info.Length
	if len(data.Info.Files) > 0 {
		file.files = make([]FileInfo, len(data.Info.Files))
		for i, fi := range data.Info.Files {
			file.files[i] = FileInfo{
				Length: fi.Length,
				Path:   fi.Path,
			}
		}
	}
	file.InfoHash = data.Info.Hash
	file.PieceLength = data.Info.PieceLength
	file.Pieces = data.Info.Pieces
	file.Private = data.Info.Private

	if len(data.AnnounceList) > 0 {
		file.AnnounceList = data.AnnounceList
	} else {
		file.AnnounceList = [][]string{[]string{data.Announce}}
	}
	file.CreationDate = time.Unix(data.CreationDate, 0)
	file.Comment = data.Comment
	file.CreatedBy = data.CreatedBy
	file.Encoding = data.Encoding
	if data.URLList != nil {
		switch v := data.URLList.(type) {
		case string:
			file.URLList = []string{v}
		case []interface{}:
			var ok bool
			ss := make([]string, len(v))
			for i, s := range v {
				ss[i], ok = s.(string)
				if !ok {
					return nil, errors.New("bad url-list data type")
				}
			}
			file.URLList = ss
		default:
			return nil, errors.New("bad url-list data type")
		}
	}
	return &file, nil
}

func OpenFromFile(filename string) (*File, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Open(f)
}

//----------------------------------------------------------------------------
// unmarshal structures
//----------------------------------------------------------------------------

type torrent_info_file struct {
	Path   []string `bencode:"path"`
	Length int64    `bencode:"length"`
	MD5Sum []byte   `bencode:"md5sum,omitempty"`
}

type torrent_info struct {
	PieceLength int64               `bencode:"piece length"`
	Pieces      []byte              `bencode:"pieces"`
	Name        string              `bencode:"name"`
	Length      int64               `bencode:"length,omitempty"`
	MD5Sum      []byte              `bencode:"md5sum,omitempty"`
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
