package test

import (
	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/torrent"
)

func init() {
	torrent.TestingTempDir.Init("torrent-test.test")
}
