package storage

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

type requiredLength struct {
	FileIndex int
	Length    int64
}

// The required file indices and file lengths for the given extent to be "complete". This is the
// outdated interface used by some tests.
func extentCompleteRequiredLengths(info *metainfo.Info, off, n int64) (ret []requiredLength) {
	index := info.FileSegmentsIndex()
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
	check := func(off, n int64, expected ...requiredLength) {
		qt.Check(t, qt.DeepEquals(extentCompleteRequiredLengths(info, off, n), expected))
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
	check := func(off, n int64, expected ...requiredLength) {
		qt.Assert(t, qt.DeepEquals(extentCompleteRequiredLengths(info, off, n), expected))
	}
	qt.Check(t, qt.HasLen(extentCompleteRequiredLengths(info, 0, 0), 0))
	qt.Check(t, qt.DeepEquals(extentCompleteRequiredLengths(info, 0, 1), []requiredLength{
		{FileIndex: 0, Length: 1},
	}))
	qt.Check(t, qt.DeepEquals(extentCompleteRequiredLengths(info, 0, 2), []requiredLength{
		{FileIndex: 0, Length: 2},
	}))
	qt.Check(t, qt.DeepEquals(extentCompleteRequiredLengths(info, 0, 3), []requiredLength{
		{FileIndex: 0, Length: 2},
		{FileIndex: 1, Length: 1},
	}))
	qt.Check(t, qt.DeepEquals(extentCompleteRequiredLengths(info, 2, 2), []requiredLength{
		{FileIndex: 1, Length: 2},
	}))
	qt.Check(t, qt.DeepEquals(extentCompleteRequiredLengths(info, 4, 1), []requiredLength{
		{FileIndex: 1, Length: 3},
	}))
	qt.Check(t, qt.HasLen(extentCompleteRequiredLengths(info, 5, 0), 0))
	check(6, 1)
}
