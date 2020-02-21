package torrent

import (
	"log"
	"os"
	"testing"

	_ "github.com/anacrolix/envpprof"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func TestMain(m *testing.M) {
	TestingTempDir.Init("torrent.test")
	code := m.Run()
	TestingTempDir.RemoveAll()
	// select {}
	os.Exit(code)
}
