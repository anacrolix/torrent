package storage_test

import (
	"testing"

	"github.com/anacrolix/torrent/storage"
	test_storage "github.com/anacrolix/torrent/storage/test"
)

func BenchmarkMarkComplete(b *testing.B) {
	bench := func(b *testing.B, ci storage.ClientImpl) {
		test_storage.BenchmarkPieceMarkComplete(
			b, ci, test_storage.DefaultPieceSize, test_storage.DefaultNumPieces, 0)
	}
	b.Run("File", func(b *testing.B) {
		ci := storage.NewFile(b.TempDir())
		b.Cleanup(func() { ci.Close() })
		bench(b, ci)
	})
	b.Run("Mmap", func(b *testing.B) {
		ci := storage.NewMMap(b.TempDir())
		b.Cleanup(func() { ci.Close() })
		bench(b, ci)
	})
	b.Run("BoltDb", func(b *testing.B) {
		ci := storage.NewBoltDB(b.TempDir())
		b.Cleanup(func() { ci.Close() })
		bench(b, ci)
	})
}
