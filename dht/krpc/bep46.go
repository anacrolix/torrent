package krpc

import "github.com/james-lawrence/torrent/metainfo"

type Bep46Payload struct {
	Ih metainfo.Hash `bencode:"ih"`
}
