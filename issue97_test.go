package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestHashPieceAfterStorageClosed(t *testing.T) {
	cl := newTestingClient(t)
	td := t.TempDir()
	cs := storage.NewFile(td)
	defer cs.Close()
	tt := cl.newTorrent(metainfo.Hash{1}, cs)
	mi := testutil.GreetingMetaInfo()
	qt.Assert(t, qt.IsNil(tt.SetInfoBytes(mi.InfoBytes)))
	go tt.piece(0).VerifyDataContext(t.Context())
	qt.Assert(t, qt.IsNil(tt.storage.Close()))
}
