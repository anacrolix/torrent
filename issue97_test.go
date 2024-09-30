package torrent

import (
	"testing"

	"github.com/anacrolix/log"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

func TestHashPieceAfterStorageClosed(t *testing.T) {
	td := t.TempDir()
	cs := storage.NewFile(td)
	defer cs.Close()
	tt := &Torrent{
		storageOpener: storage.NewClient(cs),
		logger:        log.Default,
		chunkSize:     defaultChunkSize,
	}
	tt.infoHash.Ok = true
	tt.infoHash.Value[0] = 1
	mi := testutil.GreetingMetaInfo()
	info, err := mi.UnmarshalInfo()
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(tt.setInfo(&info)))
	qt.Assert(t, qt.IsNil(tt.storage.Close()))
	tt.hashPiece(0)
}
