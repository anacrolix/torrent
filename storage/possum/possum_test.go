//go:build cgo

package possumTorrentStorage

import (
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	possum "github.com/anacrolix/possum/go"
	possumResource "github.com/anacrolix/possum/go/resource"
	_ "github.com/anacrolix/possum/go/testlink"

	"github.com/anacrolix/torrent/storage"
	test_storage "github.com/anacrolix/torrent/storage/test"
)

// This should be made to mirror the benchmarks for sqlite storage.
func BenchmarkProvider(b *testing.B) {
	possumDir, err := possum.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	possumDir.SetInstanceLimits(possum.Limits{
		DisableHolePunching: false,
		MaxValueLengthSum:   g.Some[uint64](test_storage.DefaultPieceSize * test_storage.DefaultNumPieces / 2),
	})
	defer possumDir.Close()
	possumProvider := possumResource.Provider{Handle: possumDir}
	possumTorrentProvider := Provider{Provider: possumProvider, Logger: log.Default}
	clientStorageImpl := storage.NewResourcePiecesOpts(
		possumTorrentProvider,
		storage.ResourcePiecesOpts{LeaveIncompleteChunks: true})
	test_storage.BenchmarkPieceMarkComplete(
		b,
		clientStorageImpl,
		test_storage.DefaultPieceSize,
		test_storage.DefaultNumPieces,
		0,
	)
}
