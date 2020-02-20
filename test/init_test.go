package test

import (
	"github.com/anacrolix/torrent"
)

func init() {
	torrent.TestingTempDir.Init("torrent-test.test")
}
