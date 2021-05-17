package sqliteStorage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/torrent/storage"
	test_storage "github.com/anacrolix/torrent/storage/test"
	"github.com/dustin/go-humanize"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newConnsAndProv(t *testing.T, opts NewPoolOpts) (ConnPool, *provider) {
	opts.Path = filepath.Join(t.TempDir(), "sqlite3.db")
	pool, err := NewPool(opts)
	qt.Assert(t, err, qt.IsNil)
	// sqlitex.Pool.Close doesn't like being called more than once. Let it slide for now.
	//t.Cleanup(func() { pool.Close() })
	qt.Assert(t, initPoolDatabase(pool, InitDbOpts{}), qt.IsNil)
	qt.Assert(t, initPoolConns(nil, pool, opts.InitConnOpts), qt.IsNil)
	prov, err := NewProvider(pool, ProviderOpts{BatchWrites: pool.NumConns() > 1})
	require.NoError(t, err)
	t.Cleanup(func() { prov.Close() })
	return pool, prov
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
	const pieceSize = test_storage.DefaultPieceSize
	const noTriggers = false
	var capacity int64 = test_storage.DefaultNumPieces * pieceSize / 2
	if noTriggers {
		// Since we won't push out old pieces, we have to mark them incomplete manually.
		capacity = 0
	}
	runBench := func(b *testing.B, ci storage.ClientImpl) {
		test_storage.BenchmarkPieceMarkComplete(b, ci, pieceSize, test_storage.DefaultNumPieces, capacity)
	}
	c := qt.New(b)
	b.Run("CustomDirect", func(b *testing.B) {
		var opts NewDirectStorageOpts
		opts.Capacity = capacity
		opts.NoTriggers = noTriggers
		benchOpts := func(b *testing.B) {
			opts.Path = filepath.Join(b.TempDir(), "storage.db")
			ci, err := NewDirectStorage(opts)
			c.Assert(err, qt.IsNil)
			defer ci.Close()
			runBench(b, ci)
		}
		b.Run("Default", benchOpts)
	})
	for _, memory := range []bool{false, true} {
		b.Run(fmt.Sprintf("Memory=%v", memory), func(b *testing.B) {
			b.Run("Direct", func(b *testing.B) {
				var opts NewDirectStorageOpts
				opts.Memory = memory
				opts.Capacity = capacity
				//opts.GcBlobs = true
				opts.BlobFlushInterval = time.Second
				opts.NoTriggers = noTriggers
				directBench := func(b *testing.B) {
					opts.Path = filepath.Join(b.TempDir(), "storage.db")
					ci, err := NewDirectStorage(opts)
					var ujm UnexpectedJournalMode
					if errors.As(err, &ujm) {
						b.Skipf("setting journal mode %q: %v", opts.SetJournalMode, err)
					}
					c.Assert(err, qt.IsNil)
					defer ci.Close()
					runBench(b, ci)
				}
				for _, journalMode := range []string{"", "wal", "off", "truncate", "delete", "persist", "memory"} {
					opts.SetJournalMode = journalMode
					b.Run("JournalMode="+journalMode, func(b *testing.B) {
						for _, mmapSize := range []int64{-1} {
							if memory && mmapSize >= 0 {
								continue
							}
							b.Run(fmt.Sprintf("MmapSize=%s", func() string {
								if mmapSize < 0 {
									return "default"
								} else {
									return humanize.IBytes(uint64(mmapSize))
								}
							}()), func(b *testing.B) {
								opts.MmapSize = mmapSize
								opts.MmapSizeOk = true
								directBench(b)
							})
						}
					})
				}
			})
			b.Run("ResourcePieces", func(b *testing.B) {
				for _, batchWrites := range []bool{false, true} {
					b.Run(fmt.Sprintf("BatchWrites=%v", batchWrites), func(b *testing.B) {
						var opts NewPiecesStorageOpts
						opts.Path = filepath.Join(b.TempDir(), "storage.db")
						//b.Logf("storage db path: %q", dbPath)
						opts.Capacity = capacity
						opts.Memory = memory
						opts.ProvOpts = func(opts *ProviderOpts) {
							opts.BatchWrites = batchWrites
						}
						ci, err := NewPiecesStorage(opts)
						c.Assert(err, qt.IsNil)
						defer ci.Close()
						runBench(b, ci)
					})
				}
			})
		})
	}
}
