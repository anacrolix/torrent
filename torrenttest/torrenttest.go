// Package torrenttest contains functions for testing torrent-related behaviour.
//

package torrenttest

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"hash"
	"io"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/slicesx"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

// Seeded returns the same torrent every time for the given reader.
func Seeded(dir string, n uint64, r io.Reader, options ...metainfo.Option) (info *metainfo.Info, digested hash.Hash, err error) {
	digested = md5.New()

	src, err := IOTorrent(dir, io.TeeReader(r, digested), n)
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

// Random generates a torrent from random data.
func Random(dir string, n uint64, options ...metainfo.Option) (info *metainfo.Info, digested hash.Hash, err error) {
	return Seeded(dir, n, rand.Reader, options...)
}

func RandomMulti(dir string, n int, min int64, max int64, options ...metainfo.Option) (info *metainfo.Info, err error) {
	root, err := os.MkdirTemp(dir, "multi.torrent.*")
	if err != nil {
		return nil, err
	}

	addfile := func() error {
		src, err := IOTorrent(root, rand.Reader, uint64(mrand.Int64N(max-min)+min))
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
func IOTorrent(dir string, src io.Reader, n uint64) (d *os.File, err error) {
	if d, err = os.CreateTemp(dir, "random.torrent.*.bin"); err != nil {
		return d, err
	}
	defer func() {
		if err != nil {
			os.Remove(d.Name())
		}
	}()

	if _, err = io.CopyN(d, src, int64(n)); err != nil {
		return d, err
	}

	if _, err = d.Seek(0, io.SeekStart); err != nil {
		return d, err
	}

	return d, nil
}

func RequireMessageType(t testing.TB, expected, actual btprotocol.MessageType) {
	require.Equal(t, expected, actual, "expected %s received %s", expected, actual)
}

func FilterMessageType(mt btprotocol.MessageType, msgs ...btprotocol.Message) []btprotocol.Message {
	return slicesx.Filter(func(m btprotocol.Message) bool {
		return m.Type == mt
	}, msgs...)
}

func ReadUntil(t testing.TB, m btprotocol.MessageType, reader func() (btprotocol.Message, error)) (result []btprotocol.Message, _ error) {
	ctx := t.Context()
	for {
		msg, err := reader()
		if err != nil {
			return result, err
		}

		result = append(result, msg)

		if msg.Type == m {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return result, context.Cause(ctx)
		default:
		}
	}
}
