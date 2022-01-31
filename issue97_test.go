package torrent

import (
	"testing"

	"github.com/anacrolix/log"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

func TestHashPieceAfterStorageClosed(t *testing.T) {
	td := t.TempDir()
	tt := &Torrent{
		storageOpener: storage.NewClient(storage.NewFile(td)),
		logger:        log.Default,
		chunkSize:     defaultChunkSize,
	}
	mi := testutil.GreetingMetaInfo()
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)
	require.NoError(t, tt.setInfo(&info))
	require.NoError(t, tt.storage.Close())
	tt.hashPiece(0)
}
