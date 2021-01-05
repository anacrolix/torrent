package metainfo

import (
	"bytes"
	"io"
	"os"
	"time"

	"github.com/james-lawrence/torrent/bencode"
)

type MetaInfo struct {
	InfoBytes    bencode.Bytes `bencode:"info,omitempty"`
	Announce     string        `bencode:"announce,omitempty"`
	AnnounceList AnnounceList  `bencode:"announce-list,omitempty"`
	Nodes        []Node        `bencode:"nodes,omitempty"`
	CreationDate int64         `bencode:"creation date,omitempty,ignore_unmarshal_type_error"`
	Comment      string        `bencode:"comment,omitempty"`
	CreatedBy    string        `bencode:"created by,omitempty"`
	Encoding     string        `bencode:"encoding,omitempty"`
	UrlList      UrlList       `bencode:"url-list,omitempty"`
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

func (mi MetaInfo) UnmarshalInfo() (info Info, err error) {
	err = bencode.Unmarshal(mi.InfoBytes, &info)
	return
}

func (mi MetaInfo) HashInfoBytes() (infoHash Hash) {
	return HashBytes(mi.InfoBytes)
}

// Encode to bencoded form.
func (mi MetaInfo) Write(w io.Writer) error {
	return bencode.NewEncoder(w).Encode(mi)
}

// SetDefaults set good default values in preparation for creating a new MetaInfo file.
func (mi *MetaInfo) SetDefaults() {
	mi.Comment = "yoloham"
	mi.CreatedBy = "github.com/james-lawrence/torrent"
	mi.CreationDate = time.Now().Unix()
}

// Magnet creates a Magnet from a MetaInfo.
func (mi *MetaInfo) Magnet(displayName string, infoHash Hash) (m Magnet) {
	for t := range mi.UpvertedAnnounceList().DistinctValues() {
		m.Trackers = append(m.Trackers, t)
	}
	m.DisplayName = displayName
	m.InfoHash = infoHash
	return
}

// UpvertedAnnounceList returns the announce list converted from the old single announce field if
// necessary.
func (mi *MetaInfo) UpvertedAnnounceList() AnnounceList {
	if mi.AnnounceList.OverridesAnnounce(mi.Announce) {
		return mi.AnnounceList
	}
	if mi.Announce != "" {
		return [][]string{{mi.Announce}}
	}
	return nil
}

// NodeList return nodes as a string slice.
func (mi *MetaInfo) NodeList() (ret []string) {
	ret = make([]string, len(mi.Nodes))
	for _, node := range mi.Nodes {
		ret = append(ret, string(node))
	}
	return ret
}

// Encode metainfo to store.
func Encode(mi MetaInfo) (encoded []byte, err error) {
	var (
		buf = bytes.NewBufferString("")
	)

	if err = bencode.NewEncoder(buf).Encode(mi); err != nil {
		return encoded, err
	}

	return buf.Bytes(), nil
}
