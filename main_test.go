package torrent

import (
	"log"
	"os"
	"testing"
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
