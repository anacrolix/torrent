package sqliteStorage

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/anacrolix/envpprof"
	test_storage "github.com/anacrolix/torrent/storage/test"
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
		NumConns: 3,
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
	const pieceSize = 2 << 20
	const capacity = 0
	c := qt.New(b)
	for _, memory := range []bool{false, true} {
		b.Run(fmt.Sprintf("Memory=%v", memory), func(b *testing.B) {
			dbPath := filepath.Join(b.TempDir(), "storage.db")
			//b.Logf("storage db path: %q", dbPath)
			ci, err := NewPiecesStorage(NewPiecesStorageOpts{
				NewPoolOpts: NewPoolOpts{
					Path:                  dbPath,
					Capacity:              4*pieceSize - 1,
					NoConcurrentBlobReads: false,
					PageSize:              1 << 14,
					Memory:                memory,
				},
				ProvOpts: func(opts *ProviderOpts) {
					opts.BatchWrites = true
				},
			})
			c.Assert(err, qt.IsNil)
			defer ci.Close()
			test_storage.BenchmarkPieceMarkComplete(b, ci, pieceSize, 16, capacity)
		})
	}
}
