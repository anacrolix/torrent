//go:build cgo
// +build cgo

package sqliteStorage

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/squirrel"
	"github.com/dustin/go-humanize"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/storage"
	test_storage "github.com/anacrolix/torrent/storage/test"
	"github.com/anacrolix/torrent/test"
)

func TestLeecherStorage(t *testing.T) {
	test.TestLeecherStorage(t, test.LeecherStorageTestCase{
		"SqliteDirect",
		func(s string) storage.ClientImplCloser {
			path := filepath.Join(s, "sqlite3.db")
			var opts NewDirectStorageOpts
			opts.Path = path
			cl, err := NewDirectStorage(opts)
			if err != nil {
				panic(err)
			}
			return cl
		},
		0,
	})
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
	b.Run("CustomDirect", func(b *testing.B) {
		var opts squirrel.NewCacheOpts
		opts.Capacity = capacity
		opts.NoTriggers = noTriggers
		benchOpts := func(b *testing.B) {
			opts.Path = filepath.Join(b.TempDir(), "storage.db")
			ci, err := NewDirectStorage(opts)
			qt.Assert(b, qt.IsNil(err))
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
				opts.NoTriggers = noTriggers
				directBench := func(b *testing.B) {
					opts.Path = filepath.Join(b.TempDir(), "storage.db")
					ci, err := NewDirectStorage(opts)
					var ujm squirrel.ErrUnexpectedJournalMode
					if errors.As(err, &ujm) {
						b.Skipf("setting journal mode %q: %v", opts.SetJournalMode, err)
					}
					qt.Assert(b, qt.IsNil(err))
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
		})
	}
}
