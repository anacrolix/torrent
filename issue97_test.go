package torrent

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

func TestHashPieceAfterStorageClosed(t *testing.T) {
	td, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(td)
	cs := storage.NewFile(td)
	tt := &Torrent{}
	mi := testutil.GreetingMetaInfo()
	info := mi.UnmarshalInfo()
	tt.info = &info
	tt.makePieces()
	tt.storage, err = cs.OpenTorrent(tt.info, mi.HashInfoBytes())
	require.NoError(t, err)
	require.NoError(t, tt.storage.Close())
	tt.hashPiece(0)
}
