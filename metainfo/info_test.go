package metainfo

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/internal/cryptox"
	"github.com/james-lawrence/torrent/internal/x/bytesx"
)

func TestMarshalInfo(t *testing.T) {
	var info Info
	b, err := bencode.Marshal(info)
	assert.NoError(t, err)
	assert.EqualValues(t, "d4:name0:12:piece lengthi0e6:pieces0:e", string(b))
}

func TestMarshalInfoRandom(t *testing.T) {
	info, err := NewFromReader(io.LimitReader(cryptox.NewChaCha8("test"), 128*bytesx.KiB))
	require.NoError(t, err)

	b, err := bencode.Marshal(info)
	assert.NoError(t, err)
	assert.EqualValues(t, "d6:lengthi131072e4:name32:f58493db6ea2e4369dd96d6e7ab5cec212:piece lengthi1048576e6:pieces20:q\xbcK\xb6c\x84i\x81\x80\x86\xbf\x02\xa1m:\x8f[\xe6fpe", string(b))
}

func TestOffsetTo(t *testing.T) {
	info, err := NewFromReader(io.LimitReader(cryptox.NewChaCha8("test"), 128*bytesx.KiB), OptionPieceLength(bytesx.KiB))
	require.NoError(t, err)

	require.Equal(t, int64(0), info.OffsetToIndex(0))
	require.Equal(t, int64(0), info.OffsetToIndex(1))
	require.Equal(t, int64(1), info.OffsetToIndex(bytesx.KiB))
	require.Equal(t, int64(1), info.OffsetToIndex(2*bytesx.KiB-1))
	require.Equal(t, int64(2), info.OffsetToIndex(2*bytesx.KiB))
	require.Equal(t, int64(32), info.OffsetToIndex(32*bytesx.KiB))
	require.Equal(t, int64(64), info.OffsetToIndex(64*bytesx.KiB))
	require.Equal(t, int64(127), info.OffsetToIndex(127*bytesx.KiB))
	require.Equal(t, int64(128), info.OffsetToIndex(128*bytesx.KiB))
}
