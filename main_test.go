package torrent

import (
	"io/ioutil"
	"log"
	"os"
	"testing"
)

var tempDir string

func init() {
	log.SetFlags(log.LstdFlags | log.Llongfile)
	var err error
	tempDir, err = ioutil.TempDir("", "torrent.test")
	if err != nil {
		panic(err)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(tempDir)
	// select {}
	os.Exit(code)
}
