package sqliteStorage

import (
	"bytes"
	"io"
	"io/ioutil"
	"math/rand"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newConnsAndProv(t *testing.T, opts NewPoolOpts) (ConnPool, *provider) {
	opts.Path = filepath.Join(t.TempDir(), "sqlite3.db")
	conns, provOpts, err := NewPool(opts)
	require.NoError(t, err)
	// sqlitex.Pool.Close doesn't like being called more than once. Let it slide for now.
	//t.Cleanup(func() { conns.Close() })
	prov, err := NewProvider(conns, provOpts)
	require.NoError(t, err)
	t.Cleanup(func() { prov.Close() })
	return conns, prov
}

func TestTextBlobSize(t *testing.T) {
	_, prov := newConnsAndProv(t, NewPoolOpts{})
	a, _ := prov.NewInstance("a")
	err := a.Put(bytes.NewBufferString("\x00hello"))
	qt.Assert(t, err, qt.IsNil)
	fi, err := a.Stat()
	qt.Assert(t, err, qt.IsNil)
	assert.EqualValues(t, 6, fi.Size())
}

func TestSimultaneousIncrementalBlob(t *testing.T) {
	_, p := newConnsAndProv(t, NewPoolOpts{
		NumConns:            2,
		ConcurrentBlobReads: true,
	})
	a, err := p.NewInstance("a")
	require.NoError(t, err)
	const contents = "hello, world"
	require.NoError(t, a.Put(bytes.NewReader([]byte("hello, world"))))
	rc1, err := a.Get()
	require.NoError(t, err)
	rc2, err := a.Get()
	require.NoError(t, err)
	var b1, b2 []byte
	var e1, e2 error
	var wg sync.WaitGroup
	doRead := func(b *[]byte, e *error, rc io.ReadCloser, n int) {
		defer wg.Done()
		defer rc.Close()
		*b, *e = ioutil.ReadAll(rc)
		require.NoError(t, *e, n)
		assert.EqualValues(t, contents, *b)
	}
	wg.Add(2)
	go doRead(&b2, &e2, rc2, 2)
	go doRead(&b1, &e1, rc1, 1)
	wg.Wait()
}

func BenchmarkMarkComplete(b *testing.B) {
	const pieceSize = 8 << 20
	c := qt.New(b)
	data := make([]byte, pieceSize)
	rand.Read(data)
	dbPath := filepath.Join(b.TempDir(), "storage.db")
	b.Logf("storage db path: %q", dbPath)
	ci, err := NewPiecesStorage(NewPoolOpts{Path: dbPath, Capacity: pieceSize})
	c.Assert(err, qt.IsNil)
	defer ci.Close()
	ti, err := ci.OpenTorrent(nil, metainfo.Hash{})
	c.Assert(err, qt.IsNil)
	defer ti.Close()
	pi := ti.Piece(metainfo.Piece{
		Info: &metainfo.Info{
			Pieces:      make([]byte, metainfo.HashSize),
			PieceLength: pieceSize,
			Length:      pieceSize,
		},
	})
	b.ResetTimer()
	for range iter.N(b.N) {
		storage.BenchmarkPieceMarkComplete(b, pi, data)
	}
}
