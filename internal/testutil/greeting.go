// Package testutil contains stuff for testing torrent-related behaviour.
//
// "greeting" is a single-file torrent of a file called "greeting" that
// "contains "hello, world\n".
package testutil

import (
	"crypto/rand"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/anacrolix/torrent/metainfo"
)

// Greeting torrent
var Greeting = Torrent{
	Files: []File{{
		Data: GreetingFileContents,
	}},
	Name: GreetingFileName,
}

// various constants.
const (
	GreetingFileContents = "hello, world\n"
	GreetingFileName     = "greeting"
)

// CreateDummyTorrentData in the given directory.
func CreateDummyTorrentData(dirName string) string {
	f, _ := os.Create(filepath.Join(dirName, GreetingFileName))
	defer f.Close()
	f.WriteString(GreetingFileContents)
	return f.Name()
}

// GreetingMetaInfo ...
func GreetingMetaInfo() *metainfo.MetaInfo {
	return Greeting.Metainfo(5)
}

// GreetingTestTorrent a temporary directory containing the completed "greeting" torrent,
// and a corresponding metainfo describing it. The temporary directory can be
// cleaned away with os.RemoveAll.
func GreetingTestTorrent() (tempDir string, metaInfo *metainfo.MetaInfo) {
	tempDir, err := ioutil.TempDir(os.TempDir(), "")
	if err != nil {
		panic(err)
	}
	CreateDummyTorrentData(tempDir)
	metaInfo = GreetingMetaInfo()
	return
}

// RandomDataTorrent generates a torrent from random data.
func RandomDataTorrent(dir string, n int64) (d *os.File, err error) {
	if d, err = ioutil.TempFile(dir, "random.torrent.*.bin"); err != nil {
		return d, err
	}
	defer func() {
		if err != nil {
			os.Remove(d.Name())
		}
	}()

	if _, err = io.CopyN(d, rand.Reader, n); err != nil {
		return d, err
	}

	if _, err = d.Seek(0, io.SeekStart); err != nil {
		return d, err
	}

	return d, nil
}
