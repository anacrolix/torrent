package torrent

import "github.com/anacrolix/libtorgo/metainfo"

type MetaInfo struct {
	*metainfo.Info
}

func newMetaInfo(info *metainfo.Info) *MetaInfo {
	return &MetaInfo{
		Info: info,
	}
}

func (me *MetaInfo) SingleFile() bool {
	return len(me.Info.Files) == 0
}
