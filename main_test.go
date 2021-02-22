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
	code := m.Run()
	// select {}
	os.Exit(code)
}
