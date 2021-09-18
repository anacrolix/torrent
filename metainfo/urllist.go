package metainfo

import (
	"github.com/anacrolix/torrent/bencode"
)

type UrlList []string

var (
	_ bencode.Unmarshaler = (*UrlList)(nil)
)

func (me *UrlList) UnmarshalBencode(b []byte) (err error) {
	if len(b) < 1 {
		return
	}
	if b[0] == 'l' {
		return bencode.Unmarshal(b, me)
	}
	*me = append((*me)[:0], "")
	return bencode.Unmarshal(b, &(*me)[0])
}
