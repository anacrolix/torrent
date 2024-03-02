package storage

import (
	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/segments"
	qt "github.com/frankban/quicktest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/anacrolix/torrent/metainfo"
)

type requiredLength struct {
	FileIndex int
	Length    int64
}

// The required file indices and file lengths for the given extent to be "complete". This is the
// outdated interface used by some tests.
func extentCompleteRequiredLengths(info *metainfo.Info, off, n int64) (ret []requiredLength) {
	index := segments.NewIndexFromSegments(common.TorrentOffsetFileSegments(info))
	minFileLengthsForTorrentExtent(index, off, n, func(fileIndex int, length int64) bool {
		ret = append(ret, requiredLength{fileIndex, length})
		return true
	})
	return
}

func TestExtentCompleteRequiredLengthsV2InfoWithGaps(t *testing.T) {
	info := &metainfo.Info{
		MetaVersion: 2,
		PieceLength: 2,
		FileTree: metainfo.FileTree{
			Dir: map[string]metainfo.FileTree{
				"a": {
					File: metainfo.FileTreeFile{
						Length: 2,
					},
				},
				"b": {
					File: metainfo.FileTreeFile{Length: 3},
				},
				// Here there's a gap where v2 torrents piece align, so the next file offset starts
				// at 6.
				"c": {
					File: metainfo.FileTreeFile{Length: 4},
				},
			},
		},
	}
	c := qt.New(t)
	check := func(off, n int64, expected ...requiredLength) {
		c.Check(extentCompleteRequiredLengths(info, off, n), qt.DeepEquals, expected)
	}
	check(0, 0)
	check(0, 1, requiredLength{FileIndex: 0, Length: 1})
	check(0, 2, requiredLength{FileIndex: 0, Length: 2})
	check(0, 3, requiredLength{FileIndex: 0, Length: 2}, requiredLength{FileIndex: 1, Length: 1})
	check(2, 2, requiredLength{FileIndex: 1, Length: 2})
	check(4, 1, requiredLength{FileIndex: 1, Length: 3})
	check(5, 0)
	check(4, 2, requiredLength{FileIndex: 1, Length: 3})
	check(5, 1)
	check(6, 4, requiredLength{FileIndex: 2, Length: 4})
}

func TestExtentCompleteRequiredLengths(t *testing.T) {
	info := &metainfo.Info{
		Files: []metainfo.FileInfo{
			{Path: []string{"a"}, Length: 2},
			{Path: []string{"b"}, Length: 3},
		},
	}
	c := qt.New(t)
	check := func(off, n int64, expected ...requiredLength) {
		c.Check(extentCompleteRequiredLengths(info, off, n), qt.DeepEquals, expected)
	}
	assert.Empty(t, extentCompleteRequiredLengths(info, 0, 0))
	assert.EqualValues(t, []requiredLength{
		{FileIndex: 0, Length: 1},
	}, extentCompleteRequiredLengths(info, 0, 1))
	assert.EqualValues(t, []requiredLength{
		{FileIndex: 0, Length: 2},
	}, extentCompleteRequiredLengths(info, 0, 2))
	assert.EqualValues(t, []requiredLength{
		{FileIndex: 0, Length: 2},
		{FileIndex: 1, Length: 1},
	}, extentCompleteRequiredLengths(info, 0, 3))
	assert.EqualValues(t, []requiredLength{
		{FileIndex: 1, Length: 2},
	}, extentCompleteRequiredLengths(info, 2, 2))
	assert.EqualValues(t, []requiredLength{
		{FileIndex: 1, Length: 3},
	}, extentCompleteRequiredLengths(info, 4, 1))
	assert.Len(t, extentCompleteRequiredLengths(info, 5, 0), 0)
	check(6, 1)
}
