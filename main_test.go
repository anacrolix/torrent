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
	log.SetFlags(log.LstdFlags | log.Lshortfile)
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
	code := func() int {
		defer os.RemoveAll(pkgTempDir)
		// defer profile.Start(profile.CPUProfile, profile.ProfilePath(".")).Stop()
		// defer profile.Start(profile.BlockProfile, profile.ProfilePath(".")).Stop()
		// defer profile.Start(profile.MutexProfile, profile.ProfilePath(".")).Stop()
		// defer profile.Start(profile.TraceProfile, profile.ProfilePath(".")).Stop()
		return m.Run()
	}()

	os.Exit(code)
}
