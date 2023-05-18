package torrent

import (
	"log"
	"os"
	"testing"

	_ "github.com/anacrolix/envpprof"
	analog "github.com/anacrolix/log"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	analog.DefaultTimeFormatter = analog.TimeFormatSecondsSinceInit
}

func TestMain(m *testing.M) {
	code := m.Run()
	// select {}
	os.Exit(code)
}
