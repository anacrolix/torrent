package storage

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"hash"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/md5x"
	"github.com/james-lawrence/torrent/metainfo"
)

func TestShortFile(t *testing.T) {
	td := t.TempDir()
	s := NewFile(td)
	info := &metainfo.Info{
		Name:        "a",
		Length:      2,
		PieceLength: bytesx.MiB,
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(InfoHashPathMaker(td, metainfo.Hash{}, info, nil)), 0700))
	f, err := os.Create(InfoHashPathMaker(td, metainfo.Hash{}, info, nil))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(1))
	f.Close()

	ts, err := s.OpenTorrent(info, metainfo.Hash{})
	require.NoError(t, err)

	var buf bytes.Buffer
	p := info.Piece(0)

	n, err := io.Copy(&buf, io.NewSectionReader(ts, p.Offset(), p.Length()))
	require.Equal(t, io.ErrUnexpectedEOF, err)
	require.Equal(t, int64(1), n)
}

func TestReadAllData(t *testing.T) {
	var (
		result = md5.New()
	)

	td := t.TempDir()
	s := NewFile(td)

	info, expected, err := RandomDataTorrent(td, bytesx.MiB)
	require.NoError(t, err)

	encoded, err := metainfo.Encode(info)
	require.NoError(t, err)

	ts, err := s.OpenTorrent(info, metainfo.HashBytes(encoded))
	require.NoError(t, err)

	_, err = io.Copy(result, io.NewSectionReader(ts, 0, info.Length))
	require.NoError(t, err)
	require.Equal(t, md5x.FormatHex(expected), md5x.FormatHex(result))
}

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

	id := metainfo.HashBytes(encoded)

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
