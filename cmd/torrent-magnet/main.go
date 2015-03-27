package main

import (
	"fmt"
	"os"

	"github.com/anacrolix/libtorgo/metainfo"

	"github.com/anacrolix/torrent"
)

func main() {
	mi, err := metainfo.Load(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading metainfo from stdin: %s", err)
		os.Exit(1)
	}
	ts := torrent.TorrentSpecFromMetaInfo(mi)
	m := torrent.Magnet{
		InfoHash: ts.InfoHash,
		Trackers: func() (ret []string) {
			for _, tier := range ts.Trackers {
				for _, tr := range tier {
					ret = append(ret, tr)
				}
			}
			return
		}(),
		DisplayName: ts.DisplayName,
	}
	fmt.Fprintf(os.Stdout, "%s\n", m.String())
}
