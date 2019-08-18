// +build gofuzz

package metainfo

import (
	"github.com/anacrolix/torrent/bencode"
)

func Fuzz(b []byte) int {
	var mi MetaInfo
	err := bencode.Unmarshal(b, &mi)
	if err != nil {
		return 0
	}
	_, err = bencode.Marshal(mi)
	if err != nil {
		panic(err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return 0
	}
	_, err = bencode.Marshal(info)
	if err != nil {
		panic(err)
	}
	return 1
}
