// Package testutil contains stuff for testing torrent-related behaviour.
//
// "greeting" is a single-file torrent of a file called "greeting" that
// "contains "hello, world\n".
package testutil

import (
	"crypto/md5"
	"crypto/rand"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/metainfo"
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
	f, err := os.Create(filepath.Join(dirName))
	errorsx.Panic(err)
	defer f.Close()
	f.WriteString(GreetingFileContents)
	return f.Name()
}

// GreetingMetaInfo ...
func GreetingMetaInfo() *metainfo.MetaInfo {
	return Greeting.Metainfo(5)
}

// GreetingTestTorrent a temporary directory containing the completed "greeting" torrent,
// and a corresponding metainfo describing it.
func GreetingTestTorrent(dir string) (metaInfo *metainfo.MetaInfo) {
	info := GreetingMetaInfo()
	dst := filepath.Join(dir, info.HashInfoBytes().HexString())
	CreateDummyTorrentData(dst)
	return info
}

// RandomDataTorrent generates a torrent from random data.
func RandomDataTorrent(dir string, n int64, options ...metainfo.Option) (info *metainfo.Info, digested hash.Hash, err error) {
	digested = md5.New()

	src, err := IOTorrent(dir, io.TeeReader(rand.Reader, digested), n)
	if err != nil {
		return nil, nil, err
	}
	defer src.Close()

	info, err = metainfo.NewFromPath(src.Name(), options...)

	encoded, err := metainfo.Encode(info)
	if err != nil {
		return nil, nil, err
	}

	id := metainfo.NewHashFromBytes(encoded)

	dstdir := filepath.Join(dir, id.HexString())
	if err = os.MkdirAll(filepath.Dir(dstdir), 0700); err != nil {
		return nil, nil, err
	}

	if err = os.Rename(src.Name(), dstdir); err != nil {
		return nil, nil, err
	}

	return info, digested, nil
}

// RandomDataTorrent generates a torrent from the provided io.Reader
func IOTorrent(dir string, src io.Reader, n int64) (d *os.File, err error) {
	if d, err = os.CreateTemp(dir, "random.torrent.*.bin"); err != nil {
		return d, err
	}
	defer func() {
		if err != nil {
			os.Remove(d.Name())
		}
	}()

	if _, err = io.CopyN(d, src, n); err != nil {
		return d, err
	}

	if _, err = d.Seek(0, io.SeekStart); err != nil {
		return d, err
	}

	return d, nil
}
