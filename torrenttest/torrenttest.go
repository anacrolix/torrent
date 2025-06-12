// Package torrenttest contains functions for testing torrent-related behaviour.
//

package torrenttest

import (
	"crypto/md5"
	"crypto/rand"
	"hash"
	"io"
	mrand "math/rand/v2"
	"os"
	"path/filepath"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/metainfo"
)

// RandomDataTorrent generates a torrent from random data.
func Random(dir string, n int64, options ...metainfo.Option) (info *metainfo.Info, digested hash.Hash, err error) {
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

	dstdir := filepath.Join(dir, id.String())
	if err = os.MkdirAll(filepath.Dir(dstdir), 0700); err != nil {
		return nil, nil, err
	}

	if err = os.Rename(src.Name(), dstdir); err != nil {
		return nil, nil, err
	}

	return info, digested, nil
}

func RandomMulti(dir string, n int, min int64, max int64, options ...metainfo.Option) (info *metainfo.Info, err error) {
	root, err := os.MkdirTemp(dir, "multi.torrent.*")
	if err != nil {
		return nil, err
	}

	addfile := func() error {
		src, err := IOTorrent(root, rand.Reader, mrand.Int64N(max-min)+min)
		return errorsx.Compact(err, src.Close())
	}

	for i := 0; i < n; i++ {
		if err := addfile(); err != nil {
			return nil, err
		}
	}

	info, err = metainfo.NewFromPath(root, options...)
	if err != nil {
		return nil, err
	}

	encoded, err := metainfo.Encode(info)
	if err != nil {
		return nil, err
	}

	id := metainfo.NewHashFromBytes(encoded)

	dstdir := filepath.Join(dir, id.String())
	if err = os.Rename(root, dstdir); err != nil {
		return nil, err
	}

	return info, nil
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
