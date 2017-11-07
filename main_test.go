package torrent

import (
	"io/ioutil"
	"log"
	"os"
	"testing"
)

// A top-level temp dir that lasts for the duration of the package tests, and
// is removed at completion.
var pkgTempDir string

func init() {
	log.SetFlags(log.LstdFlags | log.Llongfile)
	var err error
	pkgTempDir, err = ioutil.TempDir("", "torrent.test")
	if err != nil {
		panic(err)
	}
}

func tempDir() string {
	ret, err := ioutil.TempDir(pkgTempDir, "")
	if err != nil {
		panic(err)
	}
	return ret
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.RemoveAll(pkgTempDir)
	// select {}
	os.Exit(code)
}
