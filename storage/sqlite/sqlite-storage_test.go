package sqliteStorage

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"sync"
	"testing"

	"crawshaw.io/sqlite"
	"crawshaw.io/sqlite/sqlitex"
	_ "github.com/anacrolix/envpprof"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimultaneousIncrementalBlob(t *testing.T) {
	const poolSize = 10
	pool, err := sqlitex.Open(
		// We don't do this in memory, because it seems to have some locking issues with updating
		// last_used.
		fmt.Sprintf("file:%s", filepath.Join(t.TempDir(), "sqlite3.db")),
		// We can't disable WAL in this test because then we can't open 2 blobs simultaneously for read.
		sqlite.OpenFlagsDefault, /* &^sqlite.SQLITE_OPEN_WAL */
		poolSize)
	require.NoError(t, err)
	defer pool.Close()
	p, err := NewProviderPool(pool, poolSize, true)
	require.NoError(t, err)
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
