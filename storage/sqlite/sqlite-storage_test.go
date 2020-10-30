package sqliteStorage

import (
	"bytes"
	"io"
	"io/ioutil"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/anacrolix/envpprof"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newConnsAndProv(t *testing.T, opts NewPoolOpts) (ConnPool, *provider) {
	opts.Path = filepath.Join(t.TempDir(), "sqlite3.db")
	conns, provOpts, err := NewPool(opts)
	require.NoError(t, err)
	t.Cleanup(func() { conns.Close() })
	prov, err := NewProvider(conns, provOpts)
	require.NoError(t, err)
	return conns, prov
}

func TestTextBlobSize(t *testing.T) {
	_, prov := newConnsAndProv(t, NewPoolOpts{})
	a, _ := prov.NewInstance("a")
	a.Put(bytes.NewBufferString("\x00hello"))
	fi, _ := a.Stat()
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
