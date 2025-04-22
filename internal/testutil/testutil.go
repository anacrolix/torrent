package testutil

import (
	"hash/fnv"
	"iter"
	"math/rand"
	"os"

	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/storage"
	"github.com/stretchr/testify/require"
)

type tt interface {
	require.TestingT
	TempDir() string
}

// Autodir generates random directory under the testing temp dir.
func Autodir(t tt) string {
	dir, err := os.MkdirTemp(t.TempDir(), "")
	require.NoError(t, err)
	return dir
}

// NewBadStorage used for tests.
func NewBadStorage() storage.ClientImpl {
	return badStorage{}
}

type badStorage struct{}

func (fs badStorage) Exists() iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {}
}

func (bs badStorage) OpenTorrent(info *metainfo.Info, mihash metainfo.Hash) (storage.TorrentImpl, error) {
	var f = fnv.New64a()
	_, err := f.Write(mihash[:])
	return badStorageImpl{src: rand.New(rand.NewSource(int64(f.Sum64())))}, err
}

func (bs badStorage) Close() error {
	return nil
}

type badStorageImpl struct {
	src *rand.Rand
}

// Close implements storage.TorrentImpl.
func (p badStorageImpl) Close() error {
	return nil
}

func (p badStorageImpl) WriteAt(b []byte, off int64) (int, error) {
	return 0, nil
}

func (p badStorageImpl) ReadAt(b []byte, off int64) (n int, err error) {
	return p.src.Read(b)
}
