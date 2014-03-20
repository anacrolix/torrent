// Package testutil contains stuff for testing torrent-related behaviour.
//
// "greeting" is a single-file torrent of a file called "greeting" that
// "contains "hello, world\n".

package testutil

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	metainfo "github.com/nsf/libtorgo/torrent"

	"bytes"
)

const GreetingFileContents = "hello, world\n"

func CreateDummyTorrentData(dirName string) string {
	f, _ := os.Create(filepath.Join(dirName, "greeting"))
	f.WriteString("hello, world\n")
	return f.Name()
}

func CreateMetaInfo(name string, w io.Writer) {
	builder := metainfo.Builder{}
	builder.AddFile(name)
	builder.AddAnnounceGroup([]string{"lol://cheezburger"})
	batch, err := builder.Submit()
	if err != nil {
		panic(err)
	}
	errs, _ := batch.Start(w, 1)
	<-errs
}

// Gives a temporary directory containing the completed "greeting" torrent,
// and a corresponding metainfo describing it. The temporary directory can be
// cleaned away with os.RemoveAll.
func GreetingTestTorrent() (tempDir string, metaInfo *metainfo.MetaInfo) {
	tempDir, err := ioutil.TempDir(os.TempDir(), "")
	if err != nil {
		panic(err)
	}
	name := CreateDummyTorrentData(tempDir)
	w := &bytes.Buffer{}
	CreateMetaInfo(name, w)
	metaInfo, _ = metainfo.Load(w)
	return
}
