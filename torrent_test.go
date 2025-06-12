package torrent_test

import (
	"crypto/md5"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/internal/md5x"
	"github.com/james-lawrence/torrent/internal/testx"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/storage"
)

func TestAppendToCopySlice(t *testing.T) {
	orig := []int{1, 2, 3}
	dupe := append([]int{}, orig...)
	dupe[0] = 4
	if orig[0] != 1 {
		t.FailNow()
	}
}

// Check that a torrent containing zero-length file(s) will start, and that
// they're created in the filesystem. The client storage is assumed to be
// file-based on the native filesystem based.
func testEmptyFilesAndZeroPieceLength(t *testing.T, dir string, cfg *torrent.ClientConfig, options ...torrent.Option) {
	var (
		digest = md5.New()
	)
	ctx, done := testx.Context(t)
	defer done()

	cl, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()
	ib, err := bencode.Marshal(metainfo.Info{
		Name:        "empty",
		Length:      0,
		PieceLength: 0,
	})
	require.NoError(t, err)

	ts, err := torrent.NewFromMetaInfo(&metainfo.MetaInfo{
		InfoBytes: ib,
	}, options...)
	require.NoError(t, err)

	fp := filepath.Join(dir, ts.ID.String())
	assert.NoFileExists(t, fp)

	tt, _, err := cl.Start(ts)
	require.NoError(t, err)
	defer cl.Stop(ts)

	n, err := torrent.DownloadInto(ctx, digest, tt)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Equal(t, md5x.FormatHex(digest), testx.ReadMD5(fp))
}

func TestEmptyFilesAndZeroPieceLengthWithFileStorage(t *testing.T) {
	dir := t.TempDir()
	cfg := torrent.TestingConfig(
		t,
		torrent.ClientConfigStorageDir(dir),
	)
	ci := storage.NewFile(dir)
	defer ci.Close()
	testEmptyFilesAndZeroPieceLength(t, dir, cfg, torrent.OptionStorage(ci))
}

func TestEmptyFilesAndZeroPieceLengthWithMMapStorage(t *testing.T) {
	dir := t.TempDir()
	cfg := torrent.TestingConfig(
		t,
		torrent.ClientConfigStorageDir(dir),
	)
	ci := storage.NewMMap(dir)
	defer ci.Close()
	testEmptyFilesAndZeroPieceLength(t, dir, cfg, torrent.OptionStorage(ci))
}
