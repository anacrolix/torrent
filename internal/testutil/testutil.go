package testutil

import (
	"errors"
	"io/ioutil"
	"math/rand"
	"strings"

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
	dir, err := ioutil.TempDir(t.TempDir(), "")
	require.NoError(t, err)
	return dir
}

// NewBadStorage used for tests.
func NewBadStorage() storage.ClientImpl {
	return badStorage{}
}

type badStorage struct{}

func (bs badStorage) OpenTorrent(*metainfo.Info, metainfo.Hash) (storage.TorrentImpl, error) {
	return bs, nil
}

func (bs badStorage) Close() error {
	return nil
}

func (bs badStorage) Piece(p metainfo.Piece) storage.PieceImpl {
	return badStoragePiece{p}
}

type badStoragePiece struct {
	p metainfo.Piece
}

var _ storage.PieceImpl = badStoragePiece{}

func (p badStoragePiece) WriteAt(b []byte, off int64) (int, error) {
	return 0, nil
}

func (p badStoragePiece) Completion() storage.Completion {
	return storage.Completion{Complete: true, Ok: true}
}

func (p badStoragePiece) MarkComplete() error {
	return errors.New("psyyyyyyyche")
}

func (p badStoragePiece) MarkNotComplete() error {
	return errors.New("psyyyyyyyche")
}

func (p badStoragePiece) randomlyTruncatedDataString() string {
	return "hello, world\n"[:rand.Intn(14)]
}

func (p badStoragePiece) ReadAt(b []byte, off int64) (n int, err error) {
	r := strings.NewReader(p.randomlyTruncatedDataString())
	return r.ReadAt(b, off+p.p.Offset())
}
