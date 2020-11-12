package torrent

import (
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Specifies a new torrent for adding to a client. There are helpers for magnet URIs and torrent
// metainfo files.
type TorrentSpec struct {
	// The tiered tracker URIs.
	Trackers  [][]string
	InfoHash  metainfo.Hash
	InfoBytes []byte
	// The name to use if the Name field from the Info isn't available.
	DisplayName string
	Webseeds    []string
	DhtNodes    []string
	PeerAddrs   []string
	// The combination of the "xs" and "as" fields in magnet links, for now.
	Sources []string

	// The chunk size to use for outbound requests. Defaults to 16KiB if not set.
	ChunkSize int
	Storage   storage.ClientImpl

	// Whether to allow data download or upload
	DisallowDataUpload   bool
	DisallowDataDownload bool
}

func TorrentSpecFromMagnetUri(uri string) (spec *TorrentSpec, err error) {
	m, err := metainfo.ParseMagnetUri(uri)
	if err != nil {
		return
	}
	spec = &TorrentSpec{
		Trackers:    [][]string{m.Trackers},
		DisplayName: m.DisplayName,
		InfoHash:    m.InfoHash,
		Webseeds:    m.Params["ws"],
		Sources:     append(m.Params["xs"], m.Params["as"]...),
		PeerAddrs:   m.Params["x.pe"], // BEP 9
		// TODO: What's the parameter for DHT nodes?
	}
	return
}

func TorrentSpecFromMetaInfo(mi *metainfo.MetaInfo) *TorrentSpec {
	info, err := mi.UnmarshalInfo()
	if err != nil {
		panic(err)
	}
	return &TorrentSpec{
		Trackers:    mi.UpvertedAnnounceList(),
		InfoHash:    mi.HashInfoBytes(),
		InfoBytes:   mi.InfoBytes,
		DisplayName: info.Name,
		Webseeds:    mi.UrlList,
		DhtNodes: func() (ret []string) {
			ret = make([]string, len(mi.Nodes))
			for _, node := range mi.Nodes {
				ret = append(ret, string(node))
			}
			return
		}(),
	}
}
